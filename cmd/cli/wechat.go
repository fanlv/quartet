package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fanlv/quartet/types/model"
)

const wechatUsage = `quartet-cli wechat — WeChat (iLink) helpers

Usage:
  quartet-cli wechat <command> [flags]

Commands:
  send       Push a text message to WeChat user(s) through the backend

Run "quartet-cli wechat send -h" for command-specific flags.
`

// runWeChatGroup dispatches the `wechat` group's subcommands.
func runWeChatGroup(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, wechatUsage)
		os.Exit(2)
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "send":
		return cmdWeChatSend(rest)
	case "-h", "--help", "help":
		fmt.Print(wechatUsage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "unknown wechat command %q\n\n%s", cmd, wechatUsage)
		os.Exit(2)
		return nil
	}
}

// stringList collects a repeated flag value (e.g. --user a --user b).
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// cmdWeChatSend pushes a text message via POST /api/v1/wechat/send. Content
// comes from --file or stdin; recipients default to the backend's WeChat
// admin whitelist when no --user is given, which is what scheduled-job
// prompts want ("notify the operator").
func cmdWeChatSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	var users stringList
	fs.Var(&users, "user", "iLink user ID to send to; repeatable. Default: the backend's WeChat admin whitelist")
	file := fs.String("file", "-", `read message content from this file ("-" or empty means stdin)`)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: quartet-cli wechat send [--user <id>]... [--file <path>]\n\nPush a WeChat message through the backend. Content is read from --file, or stdin when --file is omitted or '-'.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := parseFlagsNoArgs(fs, args); err != nil {
		return err
	}

	var content []byte
	var err error
	if *file == "" || *file == "-" {
		content, err = io.ReadAll(os.Stdin)
	} else {
		content, err = os.ReadFile(*file)
	}
	if err != nil {
		return fmt.Errorf("read message content: %w", err)
	}
	if strings.TrimSpace(string(content)) == "" {
		return fmt.Errorf("message content is empty")
	}

	resp, err := newClient().sendWeChat(context.Background(), &model.WeChatSendMessageRequest{
		Content:   string(content),
		ToUserIDs: users,
	})
	if err != nil {
		return err
	}

	failed := false
	for _, r := range resp.Results {
		if r.Error != "" {
			fmt.Printf("%s\tFAILED\t%s\n", r.ToUserID, r.Error)
			failed = true
		} else {
			fmt.Printf("%s\tsent (%d chunk(s))\n", r.ToUserID, r.Chunks)
		}
	}
	if failed {
		return fmt.Errorf("some recipients failed")
	}
	return nil
}
