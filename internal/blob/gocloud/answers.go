package gocloud

import (
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// maxAnswerBytes bounds what a static key's client reads of an answer that is not an object's content. A real S3 error or
// upload answer takes a few KiB; a longer one is cut, which at most makes its error less precise.
const maxAnswerBytes = 64 << 10

// cappedAnswers is a static key's HTTP client: the store is the key's owner's, so its answers are bounded here, below the S3
// client, which reads an error's body whole before its caller sees the error. An object's content is its reader's to bound.
type cappedAnswers struct{ next aws.HTTPClient }

func (c cappedAnswers) Do(r *http.Request) (*http.Response, error) {
	resp, err := c.next.Do(r)
	if err != nil {
		return resp, err
	}
	if r.Method == http.MethodGet && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return resp, nil
	}
	resp.Body = cappedBody{io.LimitReader(resp.Body, maxAnswerBytes), resp.Body}
	return resp, nil
}

// cappedBody reads through its Reader and closes the body under it.
type cappedBody struct {
	io.Reader
	io.Closer
}
