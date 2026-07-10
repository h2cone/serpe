package provider

import (
	"context"
	"fmt"
	"io"

	"github.com/tw8ap/ouro/internal/canon"
)

// Stream performs a streaming round-trip. It returns a channel of canonical
// events; the channel closes on normal end and carries an ErrorEvent on
// in-stream failures. The upstream HTTP body is closed when the channel closes
// or the context is cancelled.
func (p *HTTPProvider) Stream(ctx context.Context, req *canon.Request) (<-chan canon.Event, error) {
	encReq := *req
	encReq.Stream = true
	body, err := p.proto.Codec.EncodeRequest(&encReq)
	if err != nil {
		return nil, err
	}

	httpReq, err := p.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s stream %s: %s", p.proto.Name, resp.Status, data)
	}

	events, err := p.proto.Codec.DecodeStream(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}

	// Wrap the codec channel so the body is closed and the context is observed.
	out := make(chan canon.Event)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				stopAndDrainStream(resp.Body, events)
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					stopAndDrainStream(resp.Body, events)
					return
				case out <- ev:
				}
			}
		}
	}()
	return out, nil
}

func stopAndDrainStream(body io.ReadCloser, events <-chan canon.Event) {
	_ = body.Close()
	go func() {
		for range events {
		}
	}()
}

// Compile-time assertion that HTTPProvider satisfies Provider.
var _ Provider = (*HTTPProvider)(nil)
