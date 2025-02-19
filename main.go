package main

import (
	"bufio"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

func main() {
	args := os.Args[1:]
	// Đường dẫn tới ứng dụng con cần chạy (exe, ví dụ)
	exePath := "./main.exe"
	// Thời gian không có output để xem xét tiến trình bị treo (ví dụ: 5 phút)
	inactivityDuration := 5 * time.Minute

	for {
		log.Println("Khởi chạy tiến trình:", exePath, "với tham số:", args)
		cmd := exec.Command(exePath, args...)

		// Tạo pipe để lấy stdout và stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Println("Lỗi khi lấy stdout pipe:", err)
			continue
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			log.Println("Lỗi khi lấy stderr pipe:", err)
			continue
		}

		// Khởi chạy tiến trình con
		if err := cmd.Start(); err != nil {
			log.Println("Lỗi khi khởi chạy tiến trình con:", err)
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
					log.Printf("Không có output trong %v, tiến trình có thể đã treo. Kill tiến trình...", inactivityDuration)
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
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
			log.Println("Tiến trình con kết thúc với lỗi:", err)
		} else {
			log.Println("Tiến trình con kết thúc bình thường.")
		}

		log.Println("Khởi chạy lại tiến trình sau 1 giây...")
		time.Sleep(1 * time.Second)
	}
}
