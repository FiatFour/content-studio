package fake

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/fiat/content-studio/internal/providers"
)

var errSpyCalled = errors.New("cloud spy called")

// CloudSpy stands in for a cloud adapter in tests of the LOCAL_ONLY rule. It implements every
// capability and counts every call, so a test can assert that none was made. The test registers
// it with providers.KindCloud; it never touches the network.
type CloudSpy struct {
	mu    sync.Mutex
	calls int
}

func (c *CloudSpy) hit() {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
}

// Calls is the number of provider methods called (Info excluded).
func (c *CloudSpy) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *CloudSpy) Info() providers.Info { return providers.Info{Provider: "cloudspy", Model: "spy"} }

func (c *CloudSpy) GenerateJSON(context.Context, string, json.RawMessage, int) ([]byte, error) {
	c.hit()
	return nil, errSpyCalled
}

func (c *CloudSpy) Generate(context.Context, providers.ImageRequest) ([]byte, error) {
	c.hit()
	return nil, errSpyCalled
}

func (c *CloudSpy) Design(context.Context, string, string, int) ([]byte, error) {
	c.hit()
	return nil, errSpyCalled
}

func (c *CloudSpy) Clone(context.Context, []byte, string, int) ([]byte, error) {
	c.hit()
	return nil, errSpyCalled
}

func (c *CloudSpy) Segment(context.Context, string) ([]string, error) {
	c.hit()
	return nil, errSpyCalled
}
