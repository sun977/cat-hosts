package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// 配置常量
const (
	// Windows 路径: C:\Windows\System32\drivers\etc\hosts
	// Mac/Linux 路径: /etc/hosts
	HostsPath = "C:\\Windows\\System32\\drivers\\etc\\hosts" 
	
	// 备份文件路径 (存放在同目录下名为 hosts.backup)
	BackupPath = "C:\\Windows\\System32\\drivers\\etc\\hosts.backup"
)

// 全局变量：日志文件路径（在启动时初始化）
var LogPath string

func main() {
	// 1. 初始化日志文件路径（当前程序所在目录）
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("获取程序路径失败: %v", err)
	}
	exeDir := filepath.Dir(exePath)
	LogPath = filepath.Join(exeDir, "hosts-monitor.log")

	// 2. 检查权限 (Windows检查管理员权限)
	if !isAdmin() {
		fmt.Println("警告:当前不是管理员权限,程序可能无法读取或写入 hosts 文件")
		fmt.Println("请以管理员权限运行此程序")
	}

	// 3. 初始化：确保备份文件存在
	if err := initBackup(); err != nil {
		log.Fatalf("初始化备份失败: %v", err)
	}

	// 4. 记录程序启动
	logEvent("程序启动", fmt.Sprintf("Hosts 监控程序启动, 用户: %s, 权限: %s", getCurrentUser(), getAdminStatus()))

	fmt.Printf("开始监控文件: %s\n", HostsPath)
	fmt.Printf("备份文件位置: %s\n", BackupPath)
	fmt.Printf("日志文件位置: %s\n", LogPath)
	fmt.Println("按 Ctrl+C 退出程序...")

	// 3. 创建监听器
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal("[!] 创建监听器失败:", err)
	}
	defer watcher.Close()

	// 4. 添加监听路径
	// 我们监听 hosts 所在的目录，因为有些编辑器修改文件时会重命名或替换文件
	dir := filepath.Dir(HostsPath)
	if err := watcher.Add(dir); err != nil {
		log.Fatal("[!] 添加监听目录失败:", err)
	}

	// 5. 处理退出信号 (Ctrl+C)
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		fmt.Println("\n正在退出...")
		os.Exit(0)
	}()

	// 6. 事件循环
	go func() {
		// 防止高频抖动，简单的防抖计时器
		var debounceTimer *time.Timer
		
		// 冷却期计时器：恢复后的一段时间内忽略新的事件
		var cooldownTimer *time.Timer
		
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				
				// 如果冷却期计时器仍在运行，说明还在冷却期内，忽略事件
				if cooldownTimer != nil {
					continue
				}
				
				// 检查是否是 hosts 文件的变化
				if filepath.Base(event.Name) != filepath.Base(HostsPath) {
					continue
				}

				// 过滤事件类型 (Write, Create, Remove, Rename 等)
				// 只要不是删除监听本身，通常都视为修改
				if event.Op&fsnotify.Write == fsnotify.Write || 
				   event.Op&fsnotify.Create == fsnotify.Create ||
				   event.Op&fsnotify.Remove == fsnotify.Remove ||
				   event.Op&fsnotify.Rename == fsnotify.Rename {
					
					fmt.Printf("[*] 检测到变化! 事件类型: %v, 文件: %s\n", event.Op, event.Name)
					
					// 获取进程信息
					processInfo := getOpenFileProcess()
					
					// 记录修改事件（包含进程信息）
					eventDesc := fmt.Sprintf("Hosts 文件被修改 - 事件类型: %v, 用户: %s, 权限: %s, 进程: %s", 
						event.Op, getCurrentUser(), getAdminStatus(), processInfo)
					logEvent("Hosts 文件修改", eventDesc)
					
					// 简单的防抖处理，防止编辑器保存时触发多次
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
					
					debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
						fmt.Println("[>>>] 发现 hosts 文件被修改，正在执行回退...")
						logEvent("开始恢复", "正在从备份恢复 hosts 文件...")
						
						if err := restoreBackup(); err != nil {
							fmt.Printf("[!] 回退失败: %v\n", err)
							logEvent("[!] 恢复失败", fmt.Sprintf("错误信息: %v", err))
						} else {
							fmt.Println("[+] 回退成功! hosts 文件已恢复。")
							logEvent("[+] 恢复成功", fmt.Sprintf("Hosts 文件已恢复, 用户: %s, 权限: %s", 
								getCurrentUser(), getAdminStatus()))
							
							// 恢复成功后，启动冷却期
							// 在这个时间内忽略新的事件，避免对恢复操作本身再次响应
							if cooldownTimer != nil {
								cooldownTimer.Stop()
							}
							cooldownTimer = time.AfterFunc(1000*time.Millisecond, func() {
								cooldownTimer = nil
								fmt.Println("[+] 监听已恢复，继续监控...")
							})
						}
					})
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				fmt.Println("[!] 监听错误:", err)
			}
		}
	}()

	// 阻塞主线程
	select {}
}

// initBackup 确保备份文件存在，如果不存在则创建
func initBackup() error {
	if _, err := os.Stat(BackupPath); os.IsNotExist(err) {
		fmt.Println("备份文件不存在，正在创建初始备份...")
		return copyFile(HostsPath, BackupPath)
	}
	return nil
}

// restoreBackup 将备份文件内容覆盖回 hosts 文件
func restoreBackup() error {
	// 1. 读取备份文件
	srcFile, err := os.Open(BackupPath)
	if err != nil {
		return fmt.Errorf("无法打开备份文件: %v", err)
	}
	defer srcFile.Close()

	// 2. 打开 hosts 文件 (覆盖写入)
	// 权限 0644 是常规文件权限，hosts 通常需要 644 或 600
	dstFile, err := os.OpenFile(HostsPath, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("无法打开 hosts 文件: %v", err)
	}
	defer dstFile.Close()

	// 3. 复制内容
	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return fmt.Errorf("写入失败: %v", err)
	}

	return nil
}

// copyFile 简单的文件复制工具
func copyFile(src, dst string) error {
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return err
	}

	if !sourceFileStat.Mode().IsRegular() {
		return fmt.Errorf("%s 不是普通文件", src)
	}

	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	_, err = io.Copy(destination, source)
	return err
}

// isAdmin 检查是否拥有管理员权限 (Windows)
func isAdmin() bool {
	_, err := os.Open("\\\\.\\PHYSICALDRIVE0")
	return err == nil
}

// getCurrentUser 获取当前用户名
func getCurrentUser() string {
	currentUser, err := user.Current()
	if err != nil {
		return "Unknown"
	}
	// 提取用户名（去掉域名前缀）
	parts := strings.Split(currentUser.Username, "\\")
	if len(parts) > 1 {
		return parts[1]
	}
	return currentUser.Username
}

// getAdminStatus 获取当前权限状态
func getAdminStatus() string {
	if isAdmin() {
		return "管理员"
	}
	return "标准用户"
}

// logEvent 记录事件到日志文件
func logEvent(eventType string, description string) {
	// 构建日志消息
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logMessage := fmt.Sprintf("[%s] [%s] %s\n", timestamp, eventType, description)
	
	// 追加写入日志文件
	file, err := os.OpenFile(LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("日志文件写入失败: %v\n", err)
		return
	}
	defer file.Close()
	
	// 写入日志
	if _, err := file.WriteString(logMessage); err != nil {
		fmt.Printf("日志文件写入失败: %v\n", err)
		return
	}
	
	// 同时输出到控制台
	fmt.Print(logMessage)
}

// getModifyingProcesses 获取修改hosts文件的进程信息
func getModifyingProcesses() string {
	// 使用 Get-Process 和 wmi 查询打开 hosts 文件的进程
	// 使用 lsof 或类似工具在 Windows 上需要额外权限
	
	// 尝试从 lsof (如果可用) 获取打开文件的进程
	processInfo := getOpenFileProcess()
	if processInfo != "" {
		return processInfo
	}
	
	return "进程信息:未知"
}

// getOpenFileProcess 通过系统接口获取打开hosts文件的进程
func getOpenFileProcess() string {
	// 尝试使用 PowerShell 的 Get-Process 和 wmi 查询
	cmd := exec.Command("powershell", "-Command", 
		`Get-Process | Where-Object { $_.Name -match 'notepad|vim|code|editor' } | Select-Object Name,Id | ForEach-Object { "$($_.Name) (PID:$($_.Id))" }`)
	
	output, err := cmd.Output()
	if err != nil {
		// 降级方案：尝试获取最后修改hosts的用户进程
		return getLastModifyingProcess()
	}
	
	result := strings.TrimSpace(string(output))
	if result != "" {
		return result
	}
	；
	return getLastModifyingProcess()
}

// getLastModifyingProcess 获取最后修改文件的进程（通过文件属性）
func getLastModifyingProcess() string {
	// 由于直接获取打开文件的进程在Go中需要系统级API调用
	// 这里使用一个近似方案：记录尝试访问的主要应用类型
	
	// 检查当前系统中常见的编辑工具
	commonEditors := []string{
		"notepad.exe",
		"notepad++.exe", 
		"vim.exe",
		"code.exe",
		"VSCode.exe",
		"sublime.exe",
		"Typora.exe",
	}
	
	for _, editor := range commonEditors {
		cmd := exec.Command("tasklist", "/FI", fmt.Sprintf(`IMAGENAME eq %s`, editor))
		output, err := cmd.Output()
		if err == nil && strings.Contains(string(output), editor) {
			// 获取进程 ID
			pidCmd := exec.Command("powershell", "-Command", 
				fmt.Sprintf(`Get-Process -Name "%s" -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty Id`, strings.TrimSuffix(editor, ".exe")))
			pidOutput, _ := pidCmd.Output()
			pid := strings.TrimSpace(string(pidOutput))
			if pid != "" {
				return fmt.Sprintf("%s (PID:%s)", editor, pid)
			}
			return editor
		}
	}
	
	return "进程:未检测到编辑器进程"
}

// logEventWithProcess 记录包含进程信息的事件
func logEventWithProcess(eventType string, description string) {
	processInfo := getOpenFileProcess()
	fullDesc := fmt.Sprintf("%s | %s", description, processInfo)
	logEvent(eventType, fullDesc)
}