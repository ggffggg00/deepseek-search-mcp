package server

import (
	"bufio"
	"bytes"
	"context"
	"io"
)

func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan []byte)
	scanErr := make(chan error, 1)

	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			payload := make([]byte, len(line))
			copy(payload, line)
			select {
			case lines <- payload:
			case <-ctx.Done():
				return
			}
		}
		scanErr <- scanner.Err()
	}()

	writer := bufio.NewWriter(out)
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				select {
				case err := <-scanErr:
					return err
				default:
					return nil
				}
			}
			resp := s.Handle(ctx, line)
			if resp == nil {
				continue
			}
			if _, err := writer.Write(resp); err != nil {
				return err
			}
			if err := writer.WriteByte('\n'); err != nil {
				return err
			}
			if err := writer.Flush(); err != nil {
				return err
			}
		}
	}
}
