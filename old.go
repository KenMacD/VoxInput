package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/gen2brain/malgo"
	"github.com/sashabaranov/go-openai"

	"github.com/richiejp/VoxInput/internal/audio"
	"github.com/richiejp/VoxInput/internal/pid"
)

func listenOld(pidPath, apiKey, httpApiBase, lang, model string, replay bool, timeout time.Duration) {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		log.Print("internal/audio: ", message)
	})
	if err != nil {
		log.Fatalln("main: ", err)
	}
	defer func() {
		_ = mctx.Uninit()
		mctx.Free()
	}()

	streamConfig := audio.StreamConfig{
		Format:       malgo.FormatS16,
		Channels:     1,
		SampleRate:   16000,
		MalgoContext: mctx.Context,
	}

	clientConfig := openai.DefaultConfig(apiKey)
	clientConfig.BaseURL = httpApiBase
	clientConfig.HTTPClient = &http.Client{
		Timeout: timeout,
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGUSR1)
	signal.Notify(sigChan, syscall.SIGUSR2)
	signal.Notify(sigChan, syscall.SIGTERM)

	err = pid.Write(pidPath)
	if err != nil {
		log.Fatalln("main: failed to write PID file: ", err)
	}

	recordingPath, err := pid.RecordingPath()
	if err != nil {
		log.Fatalln("main: failed to get recording status file path: ", err)
	}

	defer func() {
		if err := os.Remove(pidPath); err != nil {
			log.Println("main: failed to remove PID file: ", err)
		}
		_ = pid.RemoveRecordingStatus(recordingPath)
	}()

Listen:
	for {
		log.Println("main: Waiting for record signal...")
		for sig := range sigChan {
			switch sig {
			case syscall.SIGUSR1:
			case syscall.SIGUSR2:
				log.Println("main: Received stop/write signal, but wasn't recording")
				continue
			case syscall.SIGTERM:
				break Listen
			}
			break
		}

		log.Println("main: Recording...")

		// Create the recording status file when recording starts
		if err := pid.WriteRecordingStatus(recordingPath); err != nil {
			log.Println("main: failed to create recording status file: ", err)
		}

		var buf bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)

		go func() {
			if err := audio.Capture(ctx, &buf, streamConfig); err != nil {
				errCh <- fmt.Errorf("audio capture: %w", err)
			}
		}()

		for sig := range sigChan {
			switch sig {
			case syscall.SIGUSR1:
				log.Println("main: received record signal, but already recording")
				continue
			case syscall.SIGUSR2:
			case syscall.SIGTERM:
				cancel()
				break Listen
			}
			break
		}
		cancel()

		// Remove the recording status file when recording stops
		if err := pid.RemoveRecordingStatus(recordingPath); err != nil {
			log.Println("main: failed to remove recording status file: ", err)
		}

		err = <-errCh
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Println("main: error while recording: ", err)
			continue Listen
		}

		reader := bytes.NewReader(buf.Bytes())

		if replay {
			log.Println("main: Playing...")

			if err := audio.Playback(context.Background(), reader, streamConfig); err != nil && !errors.Is(err, io.EOF) {
				log.Println("main: ", fmt.Errorf("audio playback: %w", err))
			}

			log.Println("main: Playback Done")
		}

		wavHeader := audio.NewWAVHeader(uint32(buf.Len()))
		var headerBuf bytes.Buffer
		if err := wavHeader.Write(&headerBuf); err != nil {
			log.Println("main: ", fmt.Errorf("write wav header: %w", err))
			continue Listen
		}

		reader.Seek(0, io.SeekStart)
		wavReader := io.MultiReader(&headerBuf, reader)

		client := openai.NewClientWithConfig(clientConfig)
		req := openai.AudioRequest{
			Model:    model,
			FilePath: "audio.wav",
			Reader:   wavReader,
			Language: lang,
		}

		resp, err := client.CreateTranscription(context.Background(), req)
		if err != nil {
			log.Println("main: ", fmt.Errorf("CreateTranscription: %w", err))
			continue Listen
		}

		log.Println("main: transcribed text: ", resp.Text)

		dotool := exec.Command("dotool")
		stdin, err := dotool.StdinPipe()
		if err != nil {
			log.Println("main: ", fmt.Errorf("dotool stdin pipe: %w", err))
		}
		dotool.Stderr = os.Stderr
		if err := dotool.Start(); err != nil {
			log.Println("main: ", fmt.Errorf("dotool start: %w", err))
		}

		_, err = io.WriteString(stdin, fmt.Sprintf("type %s", resp.Text))
		if err != nil {
			log.Println("main: ", fmt.Errorf("dotool stdin WriteString: %w", err))
		}

		if err := stdin.Close(); err != nil {
			log.Println("main: close dotool stdin: ", err)
		}

		if err := dotool.Wait(); err != nil {
			log.Println("main: dotool wait: ", err)
		}
	}
}
