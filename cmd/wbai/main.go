// Command wbai wires Codex up to WorkBuddyAI.
//
// Subcommands:
//
//	wbai login     interactive browser login, stores credentials
//	wbai models    fetch the model payload, build the Codex catalogue
//	wbai config    patch ~/.codex/config.toml to point at the local gateway
//	wbai setup     login + models + config (the full chain)
//	wbai serve     run the local Responses API gateway
//	wbai doctor    verify that every link in the chain works
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

const defaultAddr = "127.0.0.1:8787"

// version is stamped at build time with
// -ldflags "-X main.version=v0.1.0"; it stays "dev" for plain `go build`.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		// Double-click / bare launch with a console: run the interactive
		// menu (codex-deepseek-setup style) instead of flashing usage text.
		// Piped or script callers still get plain usage + exit 2.
		if isTerminal() {
			interactiveMenu()
			return
		}
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "login":
		err = cmdLogin(args)
	case "models":
		err = cmdModels(args)
	case "config":
		err = cmdConfig(args)
	case "setup":
		err = cmdSetup(args)
	case "serve":
		err = cmdServe(args)
	case "doctor":
		err = cmdDoctor(args)
	case "version", "-v", "--version":
		fmt.Printf("wbai %s\n", version)
		return
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[ERROR] %v\n", err)
		waitExit()
		os.Exit(1)
	}
}

// menuReader is shared across menu iterations so buffered input left over
// from one read is not lost.
var menuReader = bufio.NewReader(os.Stdin)

// interactiveMenu is the double-click entry point: a numbered menu in the
// codex-deepseek-setup style. The window never closes on its own — every path
// ends in a user-confirmed Enter, and choosing exit is itself a user action.
func interactiveMenu() {
	fmt.Printf("Codex WorkBuddyAI Setup  v%s\n", version)
	fmt.Println("Storage: ~/.codex-workbuddyai-setup/")
	fmt.Printf("Gateway: http://%s/v1/responses\n\n", defaultAddr)

	for {
		fmt.Println("请选择要执行的操作：")
		fmt.Println("  1. 一键配置（登录 + 生成模型目录 + 修补 config.toml）")
		fmt.Println("  2. 启动本地网关 serve（使用 WorkBuddyAI 期间需保持运行）")
		fmt.Println("  3. 刷新模型目录（models）")
		fmt.Println("  4. 修补 config.toml（config）")
		fmt.Println("  5. 体检 doctor（检查链路每一环）")
		fmt.Println("  0. 退出")
		fmt.Println()
		fmt.Print("输入编号: ")

		line, err := menuReader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			// EOF (Ctrl+Z / Ctrl+D): the window must still not vanish —
			// ask for the final Enter, then close.
			pauseEnter("按 Enter 关闭窗口...")
			return
		}
		choice := strings.TrimSpace(line)

		switch choice {
		case "1":
			if err := cmdSetup(nil); err != nil {
				fmt.Printf("\n[ERROR] %v\n", err)
			}
		case "2":
			fmt.Println("\n网关启动后窗口会停在这一行；要停网关按 Ctrl+C，会回到菜单。")
			if err := cmdServe(nil); err != nil {
				fmt.Printf("\n[ERROR] %v\n", err)
			}
		case "3":
			if err := cmdModels(nil); err != nil {
				fmt.Printf("\n[ERROR] %v\n", err)
			}
		case "4":
			if err := cmdConfig(nil); err != nil {
				fmt.Printf("\n[ERROR] %v\n", err)
			}
		case "5":
			if err := cmdDoctor(nil); err != nil {
				// cmdDoctor already printed the per-check report.
				fmt.Printf("\n[ERROR] %v\n", err)
			}
		case "0":
			pauseEnter("按 Enter 关闭窗口...")
			return
		default:
			fmt.Println("无效输入，请输入 0-5。")
			continue
		}
		fmt.Println()
		pauseEnter("按 Enter 返回菜单...")
		fmt.Println()
	}
}

// pauseEnter blocks until the user presses Enter; the console window closes
// (or the menu re-renders) only after this returns.
func pauseEnter(prompt string) {
	fmt.Print(prompt)
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
}

func usage() {
	fmt.Print(`wbai - connect Codex to WorkBuddyAI

Usage:
  wbai login     Sign in with your browser and store the credentials
  wbai models    Fetch the model list and generate the Codex catalogue
  wbai config    Patch ~/.codex/config.toml to use the local gateway
  wbai setup     Run login + models + config in one go
  wbai serve     Start the local Responses API gateway
  wbai doctor    Check every link in the chain
  wbai version   Print the build version

Storage: ~/.codex-workbuddyai-setup/
Gateway: http://` + defaultAddr + `/v1/responses
`)
}

// waitExit only pauses on failure, and only when attached to a terminal, so the
// tool stays usable from scripts and CI.
func waitExit() {
	if !isTerminal() {
		return
	}
	fmt.Print("\nPress Enter to exit: ")
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
}

func isTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}
