package gocloud

import (
	"context"
	"io"
	"net/http"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go/middleware"

	"github.com/pyvvo/funcd/api/fault"
)

// maxAnswerBytes bounds what a static key's client reads of an answer that is neither an object's content nor a listing's
// page. A real S3 error or upload answer takes a few KiB; a longer one is cut, which at most makes its error less precise.
const maxAnswerBytes = 64 << 10

// maxListBytes is what the same client reads of all the pages of one ListAfter, however its store cuts them. A fill of 1000
// keys takes about 5.2 MiB at worst, 1,024-byte keys of a character XML escapes.
const maxListBytes = 16 << 20

// listPageCharge is the least a page spends of maxListBytes, so pages with no key end too. A fill of 1000 pages of a key each
// spends 4 MiB of it.
const listPageCharge = 4 << 10

// cappedAnswers is a static key's HTTP client. It bounds its answers here, below the S3 client, which reads an error's body
// whole before its caller sees the error. An object's content is its reader's to bound.
type cappedAnswers struct{ next aws.HTTPClient }

func (c cappedAnswers) Do(r *http.Request) (*http.Response, error) {
	resp, err := c.next.Do(r)
	if err != nil {
		return resp, err
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		switch awsmiddleware.GetOperationName(r.Context()) {
		case "GetObject":
			return resp, nil
		case "ListObjectsV2":
			resp.Body = listingBody(r.Context(), resp.Body)
			return resp, nil
		}
	}
	resp.Body = cappedBody{io.LimitReader(resp.Body, maxAnswerBytes), resp.Body}
	return resp, nil
}

type listBudgetKey struct{}

// withListBudget gives the pages asked for under ctx one budget of maxListBytes.
func withListBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, listBudgetKey{}, new(int64(maxListBytes)))
}

// listingBody reads a listing's page out of ctx's budget, or out of a budget of its own.
func listingBody(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	left, ok := ctx.Value(listBudgetKey{}).(*int64)
	if !ok {
		left = new(int64(maxListBytes))
	}
	*left -= listPageCharge
	return spendingBody{body, left}
}

// spendingBody fails a read past its budget with an error, not io.EOF: a page cut before its root would read as an empty
// listing.
type spendingBody struct {
	io.ReadCloser
	left *int64
}

func (b spendingBody) Read(p []byte) (int, error) {
	if *b.left < 0 {
		return 0, listBudgetSpent()
	}
	// One byte past the budget tells a page that ends on it from a longer one.
	if room := *b.left + 1; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := b.ReadCloser.Read(p)
	*b.left -= int64(n)
	if *b.left < 0 {
		return n - 1, listBudgetSpent()
	}
	return n, err
}

func listBudgetSpent() error {
	return fault.Invalidf("gocloud.ListObjectsV2", "the pages of one listing take more than %d bytes", maxListBytes)
}

// requireListedTimes fails a listing page that holds an object with no modification time: S3 sends one with every object,
// and the S3 driver needs it.
func requireListedTimes(stack *middleware.Stack) error {
	return stack.Deserialize.Add(middleware.DeserializeMiddlewareFunc("funcdRequireListedTimes", func(ctx context.Context,
		in middleware.DeserializeInput, next middleware.DeserializeHandler) (middleware.DeserializeOutput, middleware.Metadata, error) {
		out, md, err := next.HandleDeserialize(ctx, in)
		if err != nil {
			return out, md, err
		}
		if page, ok := out.Result.(*awss3.ListObjectsV2Output); ok &&
			slices.ContainsFunc(page.Contents, func(o s3types.Object) bool { return o.LastModified == nil }) {
			return out, md, fault.Invalidf("gocloud.ListObjectsV2", "a listed object has no modification time")
		}
		return out, md, nil
	}), middleware.Before)
}

// cappedBody reads through its Reader and closes the body under it.
type cappedBody struct {
	io.Reader
	io.Closer
}
