package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// RunTUI runs a line-oriented command interface over in and out.
func RunTUI(ctx context.Context, in io.Reader, out io.Writer, baseURL, token string) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024), maxJSONBody)
	if _, err := io.WriteString(out, "Puk 控制命令（输入 help 查看命令）\n> "); err != nil {
		return err
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			parts := strings.Fields(line)
			switch parts[0] {
			case "quit", "exit":
				return nil
			case "help":
				if len(parts) != 1 {
					if _, err := io.WriteString(out, "用法：help\n"); err != nil {
						return err
					}
				} else if _, err := io.WriteString(out, "命令：status、probe、select <selector> <tag>、stop、help、quit\n"); err != nil {
					return err
				}
			case "status":
				if len(parts) != 1 {
					if _, err := io.WriteString(out, "用法：status\n"); err != nil {
						return err
					}
					break
				}
				if err := tuiRequest(ctx, out, baseURL, token, "GET", "/api/status", nil); err != nil {
					return err
				}
			case "probe":
				if len(parts) != 1 {
					if _, err := io.WriteString(out, "用法：probe\n"); err != nil {
						return err
					}
					break
				}
				if err := tuiRequest(ctx, out, baseURL, token, "POST", "/api/probe", nil); err != nil {
					return err
				}
			case "stop":
				if len(parts) != 1 {
					if _, err := io.WriteString(out, "用法：stop\n"); err != nil {
						return err
					}
					break
				}
				if err := tuiRequest(ctx, out, baseURL, token, "POST", "/api/stop", nil); err != nil {
					return err
				}
			case "select":
				if len(parts) != 3 {
					if _, err := io.WriteString(out, "用法：select <selector> <tag>\n"); err != nil {
						return err
					}
					break
				}
				body := map[string]string{"selector": parts[1], "tag": parts[2]}
				if err := tuiRequest(ctx, out, baseURL, token, "POST", "/api/select", body); err != nil {
					return err
				}
			default:
				if _, err := io.WriteString(out, "未知命令。输入 help 查看命令。\n"); err != nil {
					return err
				}
			}
		}
		if _, err := io.WriteString(out, "> "); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return ctx.Err()
}

func tuiRequest(ctx context.Context, out io.Writer, baseURL, token, method, path string, body any) error {
	response, err := Request(ctx, baseURL, token, method, path, body)
	if err != nil {
		_, writeErr := fmt.Fprintf(out, "错误：%v\n", err)
		return writeErr
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, response, "", "  "); err != nil {
		return errors.New("invalid control API response")
	}
	_, err = fmt.Fprintln(out, pretty.String())
	return err
}
