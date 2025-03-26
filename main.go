package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config 配置结构体
type Config struct {
	CheckIntervalMinutes int
	DailyReportUTCHour   int
	MaxIncidents         int // 添加最大事件数量配置
	DingtalkWebhookToken string
	DingtalkSecret       string
}

// Incident 结构体用于解析单个事件数据
type Incident struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	MonitoringAt    time.Time `json:"monitoring_at"`
	ResolvedAt      time.Time `json:"resolved_at"`
	Impact          string    `json:"impact"`
	Shortlink       string    `json:"shortlink"`
	IncidentUpdates []Update  `json:"incident_updates"`
}

// Update 结构体用于解析事件更新数据
type Update struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Response 结构体用于解析完整的响应数据
type Response struct {
	Incidents []Incident `json:"incidents"`
}

// Service 服务结构体
type Service struct {
	config         Config
	lastIncidents  map[string]Incident
	mutex          sync.RWMutex
	lastCheckTime  time.Time
	lastReportTime time.Time
}

// 钉钉消息结构体
type DingtalkMessage struct {
	Msgtype  string `json:"msgtype"`
	Markdown struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	} `json:"markdown"`
}

// 加载配置文件
func loadConfig(configPath string) (Config, error) {
	var config Config

	file, err := os.Open(configPath)
	if err != nil {
		return config, fmt.Errorf("打开配置文件失败: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "CHECK_INTERVAL_MINUTES":
			if interval, err := strconv.Atoi(value); err == nil {
				config.CheckIntervalMinutes = interval
			}
		case "DAILY_REPORT_UTC_HOUR":
			if hour, err := strconv.Atoi(value); err == nil {
				config.DailyReportUTCHour = hour
			}
		case "MAX_INCIDENTS":
			if max, err := strconv.Atoi(value); err == nil {
				config.MaxIncidents = max
			}
		case "DINGTALK_WEBHOOK_TOKEN":
			config.DingtalkWebhookToken = value
		case "DINGTALK_SECRET":
			config.DingtalkSecret = value
		}
	}

	if err := scanner.Err(); err != nil {
		return config, fmt.Errorf("读取配置文件失败: %v", err)
	}

	// 验证必要的配置项
	if config.CheckIntervalMinutes <= 0 {
		return config, fmt.Errorf("CHECK_INTERVAL_MINUTES 必须大于0")
	}
	if config.DailyReportUTCHour < 0 || config.DailyReportUTCHour > 23 {
		return config, fmt.Errorf("DAILY_REPORT_UTC_HOUR 必须在0-23之间")
	}
	if config.MaxIncidents <= 0 {
		return config, fmt.Errorf("MAX_INCIDENTS 必须大于0")
	}
	if config.DingtalkWebhookToken == "" {
		return config, fmt.Errorf("DINGTALK_WEBHOOK_TOKEN 不能为空")
	}
	if config.DingtalkSecret == "" {
		return config, fmt.Errorf("DINGTALK_SECRET 不能为空")
	}

	return config, nil
}

func (s *Service) sendDingtalkNotification(title, content string) error {
	log.Printf("准备发送钉钉通知 - 标题: %s", title)

	message := DingtalkMessage{
		Msgtype: "markdown",
	}
	message.Markdown.Title = title
	message.Markdown.Text = content

	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Printf("生成钉钉消息 JSON 失败: %v", err)
		return err
	}
	log.Printf("钉钉消息 JSON 生成成功，长度: %d 字节", len(jsonData))

	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sign := s.generateDingtalkSign(timestamp)
	log.Printf("生成钉钉签名成功，时间戳: %s", timestamp)

	url := fmt.Sprintf("https://oapi.dingtalk.com/robot/send?access_token=%s&timestamp=%s&sign=%s",
		s.config.DingtalkWebhookToken, timestamp, sign)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("发送钉钉 HTTP 请求失败: %v", err)
		return err
	}
	defer resp.Body.Close()

	// 读取响应内容
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("读取钉钉响应失败: %v", err)
		return err
	}
	log.Printf("钉钉响应: HTTP状态码=%d, 响应内容=%s", resp.StatusCode, string(respBody))

	return nil
}

func (s *Service) generateDingtalkSign(timestamp string) string {
	stringToSign := timestamp + "\n" + s.config.DingtalkSecret
	h := hmac.New(sha256.New, []byte(s.config.DingtalkSecret))
	h.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func (s *Service) fetchAndProcessIncidents() error {
	log.Printf("开始获取 Cloudflare 状态数据...")

	resp, err := http.Get("https://www.cloudflarestatus.com/api/v2/incidents.json")
	if err != nil {
		log.Printf("HTTP 请求失败: %v", err)
		return err
	}
	defer resp.Body.Close()
	log.Printf("成功获取 HTTP 响应，状态码: %d", resp.StatusCode)

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("读取响应内容失败: %v", err)
		return err
	}
	log.Printf("成功读取响应内容，数据长度: %d 字节", len(body))

	var response Response
	if err := json.Unmarshal(body, &response); err != nil {
		log.Printf("JSON 解析失败: %v", err)
		return err
	}
	log.Printf("成功解析 JSON 数据，获取到 %d 个事件", len(response.Incidents))

	// 按时间排序
	sort.Slice(response.Incidents, func(i, j int) bool {
		return response.Incidents[i].CreatedAt.After(response.Incidents[j].CreatedAt)
	})
	log.Printf("事件按时间排序完成")

	// 检查变化并发送通知
	s.checkForChanges(response.Incidents)
	return nil
}

func (s *Service) formatIncidentDetails(incident Incident) string {
	var details strings.Builder
	details.WriteString(fmt.Sprintf("### 事件: %s\n", incident.Name))
	details.WriteString(fmt.Sprintf("- ID: %s\n", incident.ID))
	details.WriteString(fmt.Sprintf("- 状态: %s\n", incident.Status))
	details.WriteString(fmt.Sprintf("- 影响程度: %s\n", incident.Impact))
	details.WriteString(fmt.Sprintf("- 创建时间: %s\n", incident.CreatedAt.Format("2006-01-02 15:04:05")))
	details.WriteString(fmt.Sprintf("- 更新时间: %s\n", incident.UpdatedAt.Format("2006-01-02 15:04:05")))

	if !incident.MonitoringAt.IsZero() {
		details.WriteString(fmt.Sprintf("- 监控开始时间: %s\n", incident.MonitoringAt.Format("2006-01-02 15:04:05")))
	}
	if !incident.ResolvedAt.IsZero() {
		details.WriteString(fmt.Sprintf("- 解决时间: %s\n", incident.ResolvedAt.Format("2006-01-02 15:04:05")))
	}

	if len(incident.IncidentUpdates) > 0 {
		details.WriteString("\n更新历史:\n")
		for _, update := range incident.IncidentUpdates {
			details.WriteString(fmt.Sprintf("- %s [%s]: %s\n",
				update.CreatedAt.Format("2006-01-02 15:04:05"),
				update.Status,
				update.Body))
		}
	}

	if incident.Shortlink != "" {
		details.WriteString(fmt.Sprintf("\n事件链接: %s\n", incident.Shortlink))
	}

	details.WriteString("\n")
	return details.String()
}

func (s *Service) checkForChanges(incidents []Incident) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	log.Printf("开始检查事件变化...")

	// 限制事件数量为配置的最大值
	if len(incidents) > s.config.MaxIncidents {
		log.Printf("事件数量超过配置的最大值 %d，将只处理最近的 %d 个事件",
			s.config.MaxIncidents, s.config.MaxIncidents)
		incidents = incidents[:s.config.MaxIncidents]
	}
	log.Printf("当前处理的事件数量: %d", len(incidents))

	// 第一次运行时初始化并发送通知
	if s.lastIncidents == nil {
		log.Printf("首次运行，初始化事件缓存...")
		s.lastIncidents = make(map[string]Incident)

		var firstRunNotification strings.Builder
		firstRunNotification.WriteString("# Cloudflare 状态监控启动\n\n")
		firstRunNotification.WriteString(fmt.Sprintf("初始化时间: %s\n\n", time.Now().Format("2006-01-02 15:04:05")))

		if len(incidents) > 0 {
			firstRunNotification.WriteString("## 当前活跃事件\n\n")
			for _, incident := range incidents {
				log.Printf("处理初始事件 - ID: %s, 名称: %s, 状态: %s",
					incident.ID, incident.Name, incident.Status)
				s.lastIncidents[incident.ID] = incident
				firstRunNotification.WriteString(s.formatIncidentDetails(incident))
			}
		} else {
			log.Printf("初始化时没有发现活跃事件")
			firstRunNotification.WriteString("当前没有活跃的事件。\n")
		}

		firstRunNotification.WriteString("\n---\n")
		firstRunNotification.WriteString("详细状态请访问: https://www.cloudflarestatus.com/")

		log.Printf("事件缓存初始化完成，共缓存 %d 个事件", len(s.lastIncidents))

		// 发送首次运行通知
		if err := s.sendDingtalkNotification("Cloudflare 状态监控已启动", firstRunNotification.String()); err != nil {
			log.Printf("发送首次运行通知失败: %v", err)
		} else {
			log.Printf("首次运行通知发送成功")
		}
		return
	}

	var changes []string
	threeDaysAgo := time.Now().AddDate(0, 0, -3)
	log.Printf("设置时间范围：%s 之后的事件", threeDaysAgo.Format("2006-01-02 15:04:05"))

	// 检查新事件和更新
	for _, incident := range incidents {
		if !incident.CreatedAt.After(threeDaysAgo) {
			log.Printf("跳过较早的事件 - ID: %s, 创建时间: %s",
				incident.ID, incident.CreatedAt.Format("2006-01-02 15:04:05"))
			continue
		}

		oldIncident, exists := s.lastIncidents[incident.ID]
		if !exists {
			log.Printf("发现新事件 - ID: %s, 名称: %s", incident.ID, incident.Name)
			changes = append(changes, fmt.Sprintf("## 新事件\n%s", s.formatIncidentDetails(incident)))
		} else if oldIncident.UpdatedAt != incident.UpdatedAt {
			log.Printf("事件更新 - ID: %s, 名称: %s, 新状态: %s",
				incident.ID, incident.Name, incident.Status)

			// 记录状态变化
			if oldIncident.Status != incident.Status {
				log.Printf("状态变化 - ID: %s, 旧状态: %s, 新状态: %s",
					incident.ID, oldIncident.Status, incident.Status)
			}

			changes = append(changes, fmt.Sprintf("## 事件更新\n%s", s.formatIncidentDetails(incident)))
		} else {
			log.Printf("事件无变化 - ID: %s, 名称: %s", incident.ID, incident.Name)
		}
		s.lastIncidents[incident.ID] = incident
	}

	// 清理超过最大数量的旧事件
	if len(s.lastIncidents) > s.config.MaxIncidents {
		log.Printf("清理旧事件，当前缓存数量: %d，最大允许数量: %d",
			len(s.lastIncidents), s.config.MaxIncidents)
		var incidentSlice []Incident
		for _, incident := range s.lastIncidents {
			incidentSlice = append(incidentSlice, incident)
		}
		sort.Slice(incidentSlice, func(i, j int) bool {
			return incidentSlice[i].CreatedAt.After(incidentSlice[j].CreatedAt)
		})
		newIncidents := make(map[string]Incident)
		for i := 0; i < s.config.MaxIncidents && i < len(incidentSlice); i++ {
			newIncidents[incidentSlice[i].ID] = incidentSlice[i]
			log.Printf("保留事件 - ID: %s, 名称: %s",
				incidentSlice[i].ID, incidentSlice[i].Name)
		}
		s.lastIncidents = newIncidents
		log.Printf("清理完成，现有缓存数量: %d", len(s.lastIncidents))
	}

	log.Printf("事件检查完成，发现 %d 个变化", len(changes))

	// 如果有变化，发送通知
	if len(changes) > 0 {
		log.Printf("准备发送钉钉通知...")
		notification := "# Cloudflare 状态更新\n\n" +
			"时间: " + time.Now().Format("2006-01-02 15:04:05") + "\n\n" +
			strings.Join(changes, "\n") + "\n\n---\n" +
			"详细状态请访问: https://www.cloudflarestatus.com/"

		if err := s.sendDingtalkNotification("Cloudflare 状态更新", notification); err != nil {
			log.Printf("发送钉钉通知失败: %v", err)
		} else {
			log.Printf("钉钉通知发送成功")
		}
	} else {
		log.Printf("没有发现变化，跳过通知")
	}
}

func (s *Service) sendDailyReport() {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	log.Printf("开始生成每日报告...")

	var report strings.Builder
	report.WriteString("# Cloudflare 每日状态报告\n\n")
	report.WriteString(fmt.Sprintf("报告时间: %s\n\n", time.Now().Format("2006-01-02 15:04:05")))

	threeDaysAgo := time.Now().AddDate(0, 0, -3)
	hasIncidents := false
	incidentCount := 0

	log.Printf("统计 %s 之后的事件...", threeDaysAgo.Format("2006-01-02 15:04:05"))

	for _, incident := range s.lastIncidents {
		if incident.CreatedAt.After(threeDaysAgo) {
			hasIncidents = true
			incidentCount++
			log.Printf("添加事件到报告 - ID: %s, 名称: %s", incident.ID, incident.Name)
			report.WriteString(s.formatIncidentDetails(incident))
		}
	}

	log.Printf("统计完成，共有 %d 个事件", incidentCount)

	if !hasIncidents {
		log.Printf("没有发现事件")
		report.WriteString("过去三天没有发生任何事件。\n")
	}

	report.WriteString("\n---\n")
	report.WriteString("详细状态请访问: https://www.cloudflarestatus.com/")

	log.Printf("准备发送每日报告...")
	if err := s.sendDingtalkNotification("Cloudflare 每日状态报告", report.String()); err != nil {
		log.Printf("发送每日报告失败: %v", err)
	} else {
		log.Printf("每日报告发送成功")
	}
}

func (s *Service) shouldSendDailyReport() bool {
	now := time.Now().UTC()
	lastReport := s.lastReportTime.UTC()

	// 如果从未发送过报告，或者上次发送是在不同的日期
	if s.lastReportTime.IsZero() || now.Day() != lastReport.Day() {
		// 检查当前是否到达配置的发送时间
		if now.Hour() == s.config.DailyReportUTCHour {
			return true
		}
	}
	return false
}

func main() {
	// 配置日志格式
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Printf("服务启动...")

	configPath := flag.String("c", "env.config", "配置文件路径")
	flag.Parse()

	log.Printf("加载配置文件: %s", *configPath)
	config, err := loadConfig(*configPath)
	if err != nil {
		log.Printf("加载配置失败: %v", err)
		return
	}
	log.Printf("配置加载成功，检查间隔: %d 分钟，每日报告时间: UTC %d:00，最大事件数量: %d",
		config.CheckIntervalMinutes, config.DailyReportUTCHour, config.MaxIncidents)

	service := &Service{
		config: config,
	}

	// 首次运行
	log.Printf("执行首次数据获取...")
	if err := service.fetchAndProcessIncidents(); err != nil {
		log.Printf("初始化数据获取失败: %v", err)
	} else {
		log.Printf("首次数据获取成功")
	}

	ticker := time.NewTicker(time.Duration(config.CheckIntervalMinutes) * time.Minute)
	defer ticker.Stop()

	log.Printf("进入主循环，等待定时触发...")

	for {
		select {
		case <-ticker.C:
			log.Printf("定时器触发，开始新一轮检查...")
			if err := service.fetchAndProcessIncidents(); err != nil {
				log.Printf("获取数据失败: %v", err)
			} else {
				log.Printf("本轮检查完成")
			}

			if service.shouldSendDailyReport() {
				log.Printf("触发每日报告发送...")
				service.sendDailyReport()
				service.lastReportTime = time.Now()
				log.Printf("每日报告处理完成")
			}
		}
	}
}
