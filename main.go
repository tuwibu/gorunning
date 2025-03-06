package main

import (
	"bufio"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

func main() {
	args := os.Args[1:]
	// Path to the child application to run (exe, for example)
	exePath := "./main.exe"
	// Duration without output to consider process as hung (example: 5 minutes)
	inactivityDuration := 10 * time.Minute

	// Tạo channel để xử lý signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Channel để thông báo chương trình cần thoát
	exitChan := make(chan struct{})

	var currentCmd *exec.Cmd
	var cmdMutex sync.Mutex

	// Goroutine xử lý signal
	go func() {
		<-sigChan
		log.Println("Received Ctrl+C signal, exiting program...")

		cmdMutex.Lock()
		if currentCmd != nil && currentCmd.Process != nil {
			// Kill entire process tree
			pidStr := strconv.Itoa(currentCmd.Process.Pid)
			err := exec.Command("taskkill", "/T", "/F", "/PID", pidStr).Run()
			if err != nil {
				log.Println("Error executing taskkill:", err)
			} else {
				log.Println("Successfully killed process and its child processes.")
			}
		}
		cmdMutex.Unlock()

		close(exitChan)
	}()

	for {
		select {
		case <-exitChan:
			log.Println("Program terminated.")
			return
		default:
			log.Println("Starting process:", exePath, "with arguments:", args)
			cmd := exec.Command(exePath, args...)

			cmdMutex.Lock()
			currentCmd = cmd
			cmdMutex.Unlock()

			// Tạo pipe để lấy stdout và stderr
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

			// Khởi chạy tiến trình con
			if err := cmd.Start(); err != nil {
				log.Println("Error starting child process:", err)
				continue
			}

			// Channel để thông báo có output mới
			outputCh := make(chan struct{}, 1)
			wg := sync.WaitGroup{}
			wg.Add(2)

			// Hàm đọc output từ reader và gửi thông báo qua outputCh
			readOutput := func(r io.ReadCloser) {
				defer wg.Done()
				scanner := bufio.NewScanner(r)
				for scanner.Scan() {
					line := scanner.Text()
					log.Println(line)
					// Gửi tín hiệu có output mới (không block nếu channel đầy)
					select {
					case outputCh <- struct{}{}:
					default:
					}
				}
			}

			// Bắt đầu đọc stdout và stderr trong các goroutine riêng biệt
			go readOutput(stdout)
			go readOutput(stderr)

			// Goroutine giám sát inactivity
			stopMonitor := make(chan struct{})
			monitorDone := make(chan struct{})
			go func() {
				defer close(monitorDone)
				timer := time.NewTimer(inactivityDuration)
				defer timer.Stop()

				for {
					select {
					// Mỗi khi có output, reset lại timer
					case <-outputCh:
						if !timer.Stop() {
							<-timer.C
						}
						timer.Reset(inactivityDuration)
					// Nếu timer hết hạn, coi như tiến trình không có hoạt động
					case <-timer.C:
						log.Printf("No output for %v, process might be hung. Killing process...", inactivityDuration)
						if cmd.Process != nil {
							// Use taskkill to kill the entire process tree on Windows
							pidStr := strconv.Itoa(cmd.Process.Pid)
							err := exec.Command("taskkill", "/T", "/F", "/PID", pidStr).Run()
							if err != nil {
								log.Println("Error executing taskkill:", err)
							} else {
								log.Println("Successfully killed process and its child processes.")
							}
						}
						return
					case <-stopMonitor:
						return
					}
				}
			}()

			// Chờ tiến trình con kết thúc (bất kể tự thoát hay bị kill)
			err = cmd.Wait()
			// Dừng goroutine giám sát
			close(stopMonitor)
			<-monitorDone
			// Chờ các goroutine đọc output hoàn thành
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
