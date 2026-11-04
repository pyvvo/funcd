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

// maxAnswerBytes bounds what a static key's client reads of an answer that is not an object's content. A real S3 error or
// upload answer takes a few KiB; a longer one is cut, which at most makes its error less precise.
const maxAnswerBytes = 64 << 10

// maxListPageBytes bounds what the same client reads of one page of a listing. A page of 1000 keys takes under 2 MiB.
const maxListPageBytes = 16 << 20

// cappedAnswers is a static key's HTTP client. It bounds its answers here, below the S3 client, which reads an error's body
// whole before its caller sees the error. An object's content is its reader's to bound.
type cappedAnswers struct{ next aws.HTTPClient }

func (c cappedAnswers) Do(r *http.Request) (*http.Response, error) {
	resp, err := c.next.Do(r)
	if err != nil {
		return resp, err
	}
	limit := int64(maxAnswerBytes)
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		switch awsmiddleware.GetOperationName(r.Context()) {
		case "GetObject":
			return resp, nil
		case "ListObjectsV2":
			limit = maxListPageBytes
		}
	}
	resp.Body = cappedBody{io.LimitReader(resp.Body, limit), resp.Body}
	return resp, nil
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
