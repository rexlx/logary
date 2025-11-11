package logary

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Level int

const (
	DebugLevel Level = iota
	InfoLevel
	WarnLevel
	ErrorLevel
)

func (l Level) String() string {
	switch l {
	case DebugLevel:
		return "DEBUG"
	case InfoLevel:
		return "INFO"
	case WarnLevel:
		return "WARN"
	case ErrorLevel:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

type CustomLogger interface {
	Debug(args ...interface{})
	Info(args ...interface{})
	Warn(args ...interface{})
	Error(args ...interface{})
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
	DebugS(data interface{})
	InfoS(data interface{})
	WarnS(data interface{})
	ErrorS(data interface{})
}

type logEntry struct {
	level   Level
	time    time.Time
	caller  string
	message interface{}
}

type Logger struct {
	filename   string
	structured bool
	level      Level
	maxSize    int64
	maxBackups int
	logChan    chan logEntry
	doneChan   chan struct{}
	wg         sync.WaitGroup

	file   *os.File
	writer *bufio.Writer
}

type Config struct {
	Filename    string
	Structured  bool
	Level       Level
	MaxSizeMB   int
	MaxBackups  int
	BufferSize  int
	FlushFreqMS int
}

func NewLogger(config Config) (*Logger, error) {
	file, err := os.OpenFile(config.Filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file %s: %w", config.Filename, err)
	}

	if config.MaxSizeMB <= 0 {
		config.MaxSizeMB = 10
	}
	if config.MaxBackups < 0 {
		config.MaxBackups = 3
	}
	if config.BufferSize <= 0 {
		config.BufferSize = 1024
	}
	if config.FlushFreqMS <= 0 {
		config.FlushFreqMS = 1000
	}

	logger := &Logger{
		file:       file,
		writer:     bufio.NewWriter(file),
		filename:   config.Filename,
		structured: config.Structured,
		level:      config.Level,
		maxSize:    int64(config.MaxSizeMB) * 1024 * 1024,
		maxBackups: config.MaxBackups,
		logChan:    make(chan logEntry, config.BufferSize),
		doneChan:   make(chan struct{}),
	}

	logger.wg.Add(1)
	go logger.run(time.Duration(config.FlushFreqMS) * time.Millisecond)

	return logger, nil
}

const callerSkip = 2

func (l *Logger) Debug(args ...interface{}) {
	l.logInternal(DebugLevel, callerSkip, fmt.Sprint(args...))
}
func (l *Logger) Info(args ...interface{}) {
	l.logInternal(InfoLevel, callerSkip, fmt.Sprint(args...))
}
func (l *Logger) Warn(args ...interface{}) {
	l.logInternal(WarnLevel, callerSkip, fmt.Sprint(args...))
}
func (l *Logger) Error(args ...interface{}) {
	l.logInternal(ErrorLevel, callerSkip, fmt.Sprint(args...))
}
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.logInternal(DebugLevel, callerSkip, fmt.Sprintf(format, args...))
}
func (l *Logger) Infof(format string, args ...interface{}) {
	l.logInternal(InfoLevel, callerSkip, fmt.Sprintf(format, args...))
}
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.logInternal(WarnLevel, callerSkip, fmt.Sprintf(format, args...))
}
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.logInternal(ErrorLevel, callerSkip, fmt.Sprintf(format, args...))
}

func (l *Logger) DebugS(data interface{}) {
	l.logInternal(DebugLevel, callerSkip, data)
}
func (l *Logger) InfoS(data interface{}) {
	l.logInternal(InfoLevel, callerSkip, data)
}
func (l *Logger) WarnS(data interface{}) {
	l.logInternal(WarnLevel, callerSkip, data)
}
func (l *Logger) ErrorS(data interface{}) {
	l.logInternal(ErrorLevel, callerSkip, data)
}

func (l *Logger) logInternal(level Level, skip int, msg interface{}) {
	if level < l.level {
		return
	}

	var caller string
	if l.structured {
		_, file, line, ok := runtime.Caller(skip)
		if !ok {
			file = "???"
			line = 0
		}
		caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
	}

	entry := logEntry{
		level:   level,
		time:    time.Now(),
		caller:  caller,
		message: msg,
	}

	l.logChan <- entry
}

func (l *Logger) run(flushFrequency time.Duration) {
	defer l.wg.Done()
	ticker := time.NewTicker(flushFrequency)
	defer ticker.Stop()

	for {
		select {
		case entry := <-l.logChan:
			l.write(entry)

		case <-ticker.C:
			if err := l.writer.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "logger: failed periodic flush: %v\n", err)
			}

		case <-l.doneChan:
			l.shutdown()
			return
		}
	}
}

func (l *Logger) shutdown() {
	for {
		select {
		case entry := <-l.logChan:
			l.write(entry)
		default:
			if err := l.writer.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "logger: failed final flush: %v\n", err)
			}
			if err := l.file.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "logger: failed to close file: %v\n", err)
			}
			return
		}
	}
}

func (l *Logger) write(entry logEntry) {
	if err := l.checkRotate(); err != nil {
		fmt.Fprintf(os.Stderr, "logger: failed to check rotation: %v\n", err)
	}

	var logEntry []byte

	if l.structured {
		base := map[string]interface{}{
			"time":  entry.time.Format(time.RFC3339Nano),
			"level": entry.level.String(),
		}
		if entry.caller != "" {
			base["caller"] = entry.caller
		}

		var tempMap map[string]interface{}
		isMap := false

		if m, ok := entry.message.(map[string]interface{}); ok {
			tempMap = m
			isMap = true
		} else if raw, ok := entry.message.(json.RawMessage); ok {
			if err := json.Unmarshal(raw, &tempMap); err == nil {
				isMap = true
			}
		}

		if isMap {
			for k, v := range tempMap {
				if _, exists := base[k]; !exists {
					base[k] = v
				} else {
					base["msg_"+k] = v
				}
			}
		} else {
			base["message"] = entry.message
		}

		logEntry, _ = json.Marshal(base)
		logEntry = append(logEntry, '\n')

	} else {
		logEntry = []byte(fmt.Sprintf("%s [%s] %s\n", entry.time.Format(time.RFC3339), entry.level.String(), fmt.Sprint(entry.message)))
	}

	if _, err := l.writer.Write(logEntry); err != nil {
		fmt.Fprintf(os.Stderr, "logger: failed to write: %v\n", err)
	}
}

func (l *Logger) checkRotate() error {
	stat, err := l.file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() < l.maxSize {
		return nil
	}
	return l.rotate()
}

func (l *Logger) rotate() error {
	if err := l.writer.Flush(); err != nil {
		l.file.Close()
		return fmt.Errorf("failed to flush old log: %w", err)
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("failed to close old log: %w", err)
	}

	baseFilename := l.filename
	ext := filepath.Ext(baseFilename)
	prefix := strings.TrimSuffix(baseFilename, ext)

	oldestBackup := fmt.Sprintf("%s.%d%s", prefix, l.maxBackups, ext)
	os.Remove(oldestBackup)

	for i := l.maxBackups - 1; i > 0; i-- {
		src := fmt.Sprintf("%s.%d%s", prefix, i, ext)
		dst := fmt.Sprintf("%s.%d%s", prefix, i+1, ext)

		if _, err := os.Stat(src); err == nil {
			os.Rename(src, dst)
		}
	}

	backup1 := fmt.Sprintf("%s.1%s", prefix, ext)
	if err := os.Rename(l.filename, backup1); err != nil {
		fmt.Fprintf(os.Stderr, "logger: failed to rename log: %v\n", err)
	}

	newFile, err := os.OpenFile(l.filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open new log file: %w", err)
	}

	l.file = newFile
	l.writer = bufio.NewWriter(newFile)

	return nil
}

func (l *Logger) Close() error {
	close(l.doneChan)

	l.wg.Wait()
	return nil
}
