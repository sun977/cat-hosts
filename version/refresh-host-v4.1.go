package main

import (
	_ "embed"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/fsnotify/fsnotify"
	"github.com/getlantern/systray"
)

// 嵌入图标文件到程序中
//go:embed app.ico
var embeddedIcon []byte

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
var shouldExit bool = false

func main() {
	// 1. 初始化日志文件路径（当前程序所在目录）
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("获取程序路径失败: %v", err)
	}
	exeDir := filepath.Dir(exePath)
	LogPath = filepath.Join(exeDir, "hosts-monitor.log")

	// 2. 立即隐藏程序窗口（Windows 专用）- 在权限检查之前隐藏！
	hideConsoleWindow()

	// 3. 检查权限 (Windows检查管理员权限)
	if !isAdmin() {
		// 权限检查失败：显示错误提示给用户
		showErrorMessageBox("错误", "本程序需要管理员权限才能运行！\n\n请以管理员身份运行本程序。")
		logEvent("启动失败", "程序没有管理员权限，已退出")
		os.Exit(1)
	}

	// 4. 初始化：确保备份文件存在
	if err := initBackup(); err != nil {
		logEvent("错误", fmt.Sprintf("初始化备份失败: %v", err))
		return
	}

	// 5. 记录程序启动
	logEvent("程序启动", fmt.Sprintf("Hosts 监控程序启动, 用户: %s, 权限: %s", getCurrentUser(), getAdminStatus()))

	// 6. 启动系统托盘
	systray.Run(onReady, onExit)
}

// onReady 系统托盘初始化
func onReady() {
	// 加载托盘图标
	loadTrayIcon()

	// 创建托盘菜单
	mShowLog := systray.AddMenuItem("查看日志", "查看监控日志")
	systray.AddSeparator()
	mExit := systray.AddMenuItem("退出程序", "停止 Hosts 保护")

	// 6. 创建监听器
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logEvent("错误", fmt.Sprintf("创建监听器失败: %v", err))
		return
	}
	defer watcher.Close()

	// 7. 添加监听路径
	dir := filepath.Dir(HostsPath)
	if err := watcher.Add(dir); err != nil {
		logEvent("错误", fmt.Sprintf("添加监听目录失败: %v", err))
		return
	}

	logEvent("监听启动", fmt.Sprintf("开始监控 hosts 文件: %s", HostsPath))

	// 8. 后台监听文件变化
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

				// 过滤事件类型
				if event.Op&fsnotify.Write == fsnotify.Write || 
				   event.Op&fsnotify.Create == fsnotify.Create ||
				   event.Op&fsnotify.Remove == fsnotify.Remove ||
				   event.Op&fsnotify.Rename == fsnotify.Rename {
					
					// 获取进程信息
					processInfo := getOpenFileProcess()
					
					// 记录修改事件（包含进程信息）
					eventDesc := fmt.Sprintf("Hosts 文件被修改 - 事件类型: %v, 用户: %s, 权限: %s, 进程: %s", 
						event.Op, getCurrentUser(), getAdminStatus(), processInfo)
					logEvent("Hosts 文件修改", eventDesc)
					
					// 简单的防抖处理
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
					
					debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
						logEvent("开始恢复", "正在从备份恢复 hosts 文件...")
						
						if err := restoreBackup(); err != nil {
							logEvent("恢复失败", fmt.Sprintf("错误信息: %v", err))
						} else {
							logEvent("恢复成功", fmt.Sprintf("Hosts 文件已恢复, 用户: %s, 权限: %s", 
								getCurrentUser(), getAdminStatus()))
							
							// 恢复成功后，启动冷却期
							if cooldownTimer != nil {
								cooldownTimer.Stop()
							}
							cooldownTimer = time.AfterFunc(1000*time.Millisecond, func() {
								cooldownTimer = nil
							})
						}
					})
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				logEvent("监听错误", fmt.Sprintf("错误: %v", err))
			}
		}
	}()

	// 9. 处理菜单点击事件
	for {
		select {
		case <-mShowLog.ClickedCh:
			openLogFile()
		case <-mExit.ClickedCh:
			shouldExit = true
			systray.Quit()
			return
		}
	}
}

// onExit 程序退出
func onExit() {
	logEvent("程序退出", "Hosts 监控程序已退出")
	os.Exit(0)
}

// showErrorMessageBox 显示 Windows 错误对话框
func showErrorMessageBox(title, message string) {
	// 仅在 Windows 上执行
	if runtime.GOOS != "windows" {
		return
	}
	
	// 使用 Windows API 显示消息框
	user32 := syscall.NewLazyDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	
	// 将字符串转换为 UTF-16（Windows API 使用的格式）
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	messagePtr, _ := syscall.UTF16PtrFromString(message)
	
	// MB_ICONERROR = 0x10, MB_OK = 0x00
	messageBox.Call(0, uintptr(unsafe.Pointer(messagePtr)), uintptr(unsafe.Pointer(titlePtr)), 0x10|0x00)
}

// hideConsoleWindow 隐藏 Windows 控制台窗口
func hideConsoleWindow() {
	// 仅在 Windows 上执行
	if runtime.GOOS != "windows" {
		return
	}
	
	getConsoleWindow := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")
	showWindow := syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow")
	
	hwnd, _, _ := getConsoleWindow.Call()
	
	// SW_HIDE = 0
	showWindow.Call(hwnd, 0)
}

// initBackup 确保备份文件存在，如果不存在则创建
func initBackup() error {
	if _, err := os.Stat(BackupPath); os.IsNotExist(err) {
		logEvent("备份", "备份文件不存在，正在创建初始备份...")
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
		// 静默失败，不输出到控制台（因为窗口已隐藏）
		return
	}
	defer file.Close()
	
	// 写入日志
	if _, err := file.WriteString(logMessage); err != nil {
		// 静默失败
		return
	}
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

// openLogFile 打开日志文件
func openLogFile() {
	cmd := exec.Command("notepad.exe", LogPath)
	cmd.Start()
}

// loadTrayIcon 加载嵌入的托盘图标
func loadTrayIcon() {
	// 使用编译时嵌入的图标数据
	if len(embeddedIcon) == 0 {
		logEvent("图标加载", "嵌入的图标数据为空，使用默认图标")
		return
	}
	
	// 设置托盘图标
	systray.SetIcon(embeddedIcon)
	logEvent("图标加载", fmt.Sprintf("自定义图标已加载 (编译嵌入, 大小: %d 字节)", len(embeddedIcon)))
}
