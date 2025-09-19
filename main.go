package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/richiejp/VoxInput/internal/gui"
	"github.com/richiejp/VoxInput/internal/pid"
	"github.com/richiejp/VoxInput/internal/semver"
)

//go:embed version.txt
var version []byte

func main() {

	if err := semver.SetVersion(version); err != nil {
		fmt.Println("Version format error '%s': %v", string(version), err)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		fmt.Println("Expected 'listen', 'record', 'write', 'status', or 'help' subcommands")
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "help":
		fmt.Println("Available commands:")
		fmt.Println("  listen - Start speech to text daemon")
		fmt.Println("           --replay play the audio just recorded for transcription")
		fmt.Println("           --no-realtime use the HTTP API instead of the realtime API; disables VAD")
		fmt.Println("           --no-show-status don't show when recording has started or stopped")
		fmt.Println("  record - Tell existing listener to start recording audio. In realtime mode it also begins transcription")
		fmt.Println("  write  - Tell existing listener to stop recording audio and begin transcription if not in realtime mode")
		fmt.Println("  stop   - Alias for write; makes more sense in realtime mode")
		fmt.Println("  status - Check if the listener is currently recording")
		fmt.Println("  help   - Show this help message")
		fmt.Println("  ver    - Print version")
		return
	case "ver":
		fmt.Printf("v%s\n", strings.TrimSpace(string(version)))
		return
	default:
	}

	pidPath, err := pid.Path()
	if err != nil {
		log.Fatalln("main: failed to get PID file path: ", err)
	}

	if cmd == "listen" {
		apiKey := getOpenaiEnv("API_KEY", "sk-xxx")
		httpApiBase := getOpenaiEnv("BASE_URL", "http://localhost:8080/v1")
		wsApiBase := getOpenaiEnv("WS_BASE_URL", "ws://localhost:8080/v1/realtime")
		lang := getPrefixedEnv([]string{"VOXINPUT", ""}, "LANG", "")
		model := getPrefixedEnv([]string{"VOXINPUT", ""}, "TRANSCRIPTION_MODEL", "whisper-1")
		timeoutStr := getPrefixedEnv([]string{"VOXINPUT", ""}, "TRANSCRIPTION_TIMEOUT", "30s")
		showStatus := getPrefixedEnv([]string{"VOXINPUT", ""}, "SHOW_STATUS", "yes")

		timeout, err := time.ParseDuration(timeoutStr)
		if err != nil {
			log.Println("main: failed to parse timeout", err)
			timeout = time.Second * 30
		}

		if len(lang) > 2 {
			lang = lang[:2]
		}

		if lang != "" {
			log.Println("main: language is set to ", lang)
		}

		if showStatus == "no" || showStatus == "false" {
			showStatus = ""
		}

		if slices.Contains(os.Args[2:], "--no-show-status") {
			showStatus = ""
		}

		replay := slices.Contains(os.Args[2:], "--replay")
		realtime := !slices.Contains(os.Args[2:], "--no-realtime")

		if realtime {
			ctx, cancel := context.WithCancel(context.Background())
			ui := gui.New(ctx, showStatus)

			go func() {
				listen(pidPath, apiKey, httpApiBase, wsApiBase, lang, model, timeout, ui)
				cancel()
			}()

			ui.Run()
		} else {
			listenOld(pidPath, apiKey, httpApiBase, lang, model, replay, timeout)
		}

		return
	}

	id, err := pid.Read(pidPath)
	if err != nil {
		log.Fatalln("main: failed to read listener PID: ", err)
	}

	proc, err := os.FindProcess(id)
	if err != nil {
		log.Fatalln("main: Failed to find listen process: ", err)
	}

	switch cmd {
	case "record":
		log.Println("main: Sending record signal")
		err = proc.Signal(syscall.SIGUSR1)
	case "stop":
		fallthrough
	case "write":
		log.Println("main: Sending stop/write signal")
		err = proc.Signal(syscall.SIGUSR2)
	case "status":
		// Check if the process is running by sending signal 0
		err = proc.Signal(syscall.Signal(0))
		if err != nil {
			fmt.Println("not running")
			os.Exit(1)
		}

		// Check if the service is currently recording
		recordingPath, err := pid.RecordingPath()
		if err != nil {
			log.Println("main: failed to get recording status file path: ", err)
			fmt.Println("running")
			return
		}

		// Try to read the recording status file
		if _, err := os.Stat(recordingPath); err == nil {
			fmt.Println("recording")
		} else if os.IsNotExist(err) {
			fmt.Println("running")
		} else {
			// Some other error occurred
			log.Println("main: error checking recording status file: ", err)
			fmt.Println("running")
		}
		return
	default:
		log.Fatalln("main: Unknown command: ", os.Args[1])
	}

	if err != nil {
		log.Fatalln("main: Error sending signal: ", err)
	}
}
