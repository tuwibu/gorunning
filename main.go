package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func parseAutoRestart(args []string) ([]string, int) {
	var filteredArgs []string
	autoRestartSeconds := 0

	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			filteredArgs = append(filteredArgs, arg)
			continue
		}

		parts := strings.SplitN(strings.TrimPrefix(arg, "--"), "=", 2)
		if len(parts) != 2 {
			filteredArgs = append(filteredArgs, arg)
			continue
		}

		key, value := parts[0], parts[1]
		if key == "auto-restart" {
			if val, err := strconv.Atoi(value); err == nil {
				autoRestartSeconds = val
			} else {
				log.Printf("Warning: Invalid auto-restart value: %s", value)
			}
			continue
		}

		filteredArgs = append(filteredArgs, arg)
	}

	return filteredArgs, autoRestartSeconds
}

func main() {
	args, autoRestartSeconds := parseAutoRestart(os.Args[1:])
	exePath := "./main.exe"
	inactivityDuration := 10 * time.Minute

	log.Printf("Auto-restart interval: %d seconds", autoRestartSeconds)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	exitChan := make(chan struct{})

	var currentCmd *exec.Cmd
	var cmdMutex sync.Mutex

	go func() {
		<-sigChan
		log.Println("Received shutdown signal. Sending graceful shutdown to main.exe...")

		cmdMutex.Lock()
		if currentCmd != nil && currentCmd.Process != nil {
			// Gửi tín hiệu nhẹ nhàng: taskkill không /F
			pidStr := strconv.Itoa(currentCmd.Process.Pid)
			err := exec.Command("taskkill", "/PID", pidStr).Run()
			if err != nil {
				log.Printf("Error sending shutdown signal to main.exe: %v", err)
			} else {
				log.Println("Sent shutdown signal to main.exe. Waiting for graceful shutdown...")
			}
		}
		cmdMutex.Unlock()

		// Đợi main.exe thoát
		cmdMutex.Lock()
		if currentCmd != nil {
			currentCmd.Wait() // Đợi main.exe tự thoát
		}
		cmdMutex.Unlock()

		close(exitChan)
	}()

	for {
		select {
		case <-exitChan:
			log.Println("Wrapper exiting after graceful main.exe shutdown.")
			return
		default:
			log.Println("Starting process:", exePath, "with arguments:", args)
			cmd := exec.Command(exePath, args...)

			cmdMutex.Lock()
			currentCmd = cmd
			cmdMutex.Unlock()

			stdout, err := cmd.StdoutPipe()
			if err != nil {
				log.Println("Error getting stdout pipe:", err)
				continue
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				log.Println("Error getting stderr pipe:", err)
				continue
			}

			if err := cmd.Start(); err != nil {
				log.Println("Error starting child process:", err)
				continue
			}

			outputCh := make(chan struct{}, 1)
			wg := sync.WaitGroup{}
			wg.Add(2)

			readOutput := func(r io.ReadCloser) {
				defer wg.Done()
				scanner := bufio.NewScanner(r)
				for scanner.Scan() {
					line := scanner.Text()
					fmt.Println(line)
					select {
					case outputCh <- struct{}{}:
					default:
					}
				}
			}

			go readOutput(stdout)
			go readOutput(stderr)

			stopMonitor := make(chan struct{})
			monitorDone := make(chan struct{})

			if autoRestartSeconds > 0 {
				go func() {
					timer := time.NewTimer(time.Duration(autoRestartSeconds) * time.Second)
					defer timer.Stop()

					select {
					case <-timer.C:
						log.Printf("Auto-restart timer (%d seconds) expired. Requesting main.exe to stop...", autoRestartSeconds)
						if cmd.Process != nil {
							pidStr := strconv.Itoa(cmd.Process.Pid)
							_ = exec.Command("taskkill", "/PID", pidStr).Run()
						}
					case <-stopMonitor:
						return
					}
				}()
			}

			go func() {
				defer close(monitorDone)
				timer := time.NewTimer(inactivityDuration)
				defer timer.Stop()

				for {
					select {
					case <-outputCh:
						if !timer.Stop() {
							<-timer.C
						}
						timer.Reset(inactivityDuration)
					case <-timer.C:
						log.Printf("No output for %v, requesting main.exe stop...", inactivityDuration)
						if cmd.Process != nil {
							pidStr := strconv.Itoa(cmd.Process.Pid)
							_ = exec.Command("taskkill", "/PID", pidStr).Run()
						}
						return
					case <-stopMonitor:
						return
					}
				}
			}()

			err = cmd.Wait()
			close(stopMonitor)
			<-monitorDone
			wg.Wait()

			if err != nil {
				log.Println("Child process terminated with error:", err)
			} else {
				log.Println("Child process terminated normally.")
			}

			cmdMutex.Lock()
			currentCmd = nil
			cmdMutex.Unlock()

			log.Println("Restarting process in 1 second...")
			time.Sleep(1 * time.Second)
		}
	}
}
