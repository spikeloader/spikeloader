package sshx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"time"

	"spikeloader/internal/agent"
)

func AgentFrames(ctx context.Context, method, path string, body io.Reader, opt Stream) (int, error) {
	resp, err := AgentRequest(ctx, method, path, body)
	if err != nil {
		return 1, err
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	for {
		var frame agent.Chunk
		if err := decoder.Decode(&frame); err != nil {
			return 1, fmt.Errorf("agent stream: %w", err)
		}
		if len(frame.Data) > 0 {
			if opt.Stdout != nil {
				if _, err := opt.Stdout.Write(frame.Data); err != nil {
					return 1, err
				}
			} else if opt.OnData != nil {
				opt.OnData(frame.Data)
			}
		}
		if len(frame.Stderr) > 0 && opt.OnData != nil {
			opt.OnData(frame.Stderr)
		}
		if frame.Code != nil {
			return *frame.Code, nil
		}
	}
}

func AgentExec(ctx context.Context, vm int, command string, opt Stream) int {
	if opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.Timeout)
		defer cancel()
	}
	var body io.Reader
	if opt.Stdin != "" {
		f, err := os.Open(opt.Stdin)
		if err != nil {
			emit(opt.OnData, err.Error()+"\n")
			return 1
		}
		defer f.Close()
		body = f
	}
	code, err := AgentFrames(ctx, "POST", fmt.Sprintf("/vms/%d/exec?command=%s", vm, url.QueryEscape(command)), body, opt)
	if err != nil {
		emit(opt.OnData, err.Error()+"\n")
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	return code
}

func AgentFollow(ctx context.Context, id string, onData func([]byte)) (int, error) {
	offset := int64(0)
	for {
		resp, err := AgentRequest(ctx, "GET", "/jobs/"+id+"/log?offset="+strconv.FormatInt(offset, 10), nil)
		if err == nil {
			decoder := json.NewDecoder(resp.Body)
			for {
				var frame agent.Chunk
				if err = decoder.Decode(&frame); err != nil {
					break
				}
				if len(frame.Data) > 0 {
					if frame.Offset != offset+int64(len(frame.Data)) {
						err = fmt.Errorf("invalid agent log offset %d", frame.Offset)
						break
					}
					onData(frame.Data)
					offset = frame.Offset
				}
				if frame.Code != nil {
					resp.Body.Close()
					return *frame.Code, nil
				}
			}
			resp.Body.Close()
		}
		if ctx.Err() != nil {
			return 130, ctx.Err()
		}
		if ae, ok := err.(*AgentError); ok && ae.Status == 404 {
			return 1, err
		}
		// Reconnect only on a broken stream, never poll a running job.
		select {
		case <-ctx.Done():
			return 130, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
