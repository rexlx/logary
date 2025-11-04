package main

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

// Level defines the log level.
type Level int

const (
	DebugLevel Level = iota
	InfoLevel
	WarnLevel
	ErrorLevel
)

// String returns the string representation of a log level.
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

// CustomLogger defines the logger interface.
type CustomLogger interface {
	Debug(args ...interface{})
	Info(args ...interface{})
	Warn(args ...interface{})
	Error(args ...interface{})
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// logEntry represents a single log message.
type logEntry struct {
	level   Level
	time    time.Time
	caller  string
	message string
}

// Logger is a thread-safe, asynchronous logger.
type Logger struct {
	// --- Atomic/Immutable fields (set at creation) ---
	filename   string
	structured bool
	level      Level
	maxSize    int64 // in bytes
	maxBackups int
	logChan    chan logEntry
	doneChan   chan struct{}
	wg         sync.WaitGroup

	// --- Fields protected by the internal goroutine ---
	file   *os.File
	writer *bufio.Writer
}

// Config stores logger configuration.
type Config struct {
	Filename    string // Log filename
	Structured  bool   // Use JSON format
	Level       Level  // Minimum log level
	MaxSizeMB   int    // Max size in MB before rotation
	MaxBackups  int    // Max number of old log files to keep
	BufferSize  int    // Size of the internal log channel
	FlushFreqMS int    // How often to flush to disk (in ms)
}

// NewLogger creates a new asynchronous logger instance.
func NewLogger(config Config) (*Logger, error) {
	file, err := os.OpenFile(config.Filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file %s: %w", config.Filename, err)
	}

	// Default values
	if config.MaxSizeMB <= 0 {
		config.MaxSizeMB = 10
	}
	if config.MaxBackups < 0 {
		config.MaxBackups = 3
	}
	if config.BufferSize <= 0 {
		config.BufferSize = 1024 // 1024 buffered log messages
	}
	if config.FlushFreqMS <= 0 {
		config.FlushFreqMS = 1000 // 1 second
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

	// Start the background logging goroutine
	logger.wg.Add(1)
	go logger.run(time.Duration(config.FlushFreqMS) * time.Millisecond)

	return logger, nil
}

// --- Public Log Methods ---

func (l *Logger) Debug(args ...interface{}) {
	l.log(DebugLevel, fmt.Sprint(args...))
}
func (l *Logger) Info(args ...interface{}) {
	l.log(InfoLevel, fmt.Sprint(args...))
}
func (l *Logger) Warn(args ...interface{}) {
	l.log(WarnLevel, fmt.Sprint(args...))
}
func (l *Logger) Error(args ...interface{}) {
	l.log(ErrorLevel, fmt.Sprint(args...))
}
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.log(DebugLevel, fmt.Sprintf(format, args...))
}
func (l *Logger) Infof(format string, args ...interface{}) {
	l.log(InfoLevel, fmt.Sprintf(format, args...))
}
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.log(WarnLevel, fmt.Sprintf(format, args...))
}
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.log(ErrorLevel, fmt.Sprintf(format, args...))
}

// --- Core Logic ---

// log is the public, non-blocking method.
// It formats the entry and sends it to the channel.
func (l *Logger) log(level Level, msg string) {
	if level < l.level {
		return
	}

	// Get caller info
	var caller string
	if l.structured {
		_, file, line, ok := runtime.Caller(2) // 2 steps up: log -> Debug/Info... -> caller
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

	// Send to channel. This will block if the channel is full.
	// For a more robust library, a select with a default
	// could drop logs instead of blocking the application.
	l.logChan <- entry
}

// run is the background goroutine that handles all file I/O.
func (l *Logger) run(flushFrequency time.Duration) {
	defer l.wg.Done()
	ticker := time.NewTicker(flushFrequency)
	defer ticker.Stop()

	for {
		select {
		case entry := <-l.logChan:
			// A log message was received
			l.write(entry)

		case <-ticker.C:
			// Periodic flush
			if err := l.writer.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "logger: failed periodic flush: %v\n", err)
			}

		case <-l.doneChan:
			// Shutdown signal
			l.shutdown()
			return
		}
	}
}

// shutdown drains the channel, flushes, and closes the file.
func (l *Logger) shutdown() {
	// Stop accepting new logs (though channel is already closed by Close())
	// Drain any remaining items in the log channel
	for {
		select {
		case entry := <-l.logChan:
			l.write(entry)
		default:
			// Channel is empty, proceed to final flush and close
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

// write is the internal, unsafe method that writes to the buffer.
// It is ONLY called by the `run` goroutine.
func (l *Logger) write(entry logEntry) {
	// Check if rotation is needed
	if err := l.checkRotate(); err != nil {
		fmt.Fprintf(os.Stderr, "logger: failed to check rotation: %v\n", err)
	}

	var logEntry []byte

	if l.structured {
		jsonEntry := struct {
			Time    string `json:"time"`
			Level   string `json:"level"`
			Message string `json:"message"`
			Caller  string `json:"caller,omitempty"`
		}{
			Time:    entry.time.Format(time.RFC3339Nano),
			Level:   entry.level.String(),
			Message: entry.message,
			Caller:  entry.caller,
		}
		logEntry, _ = json.Marshal(jsonEntry)
		logEntry = append(logEntry, '\n')

	} else {
		// Plain text format
		logEntry = []byte(fmt.Sprintf("%s [%s] %s\n", entry.time.Format(time.RFC3339), entry.level.String(), entry.message))
	}

	// Write to buffer (not flushed immediately)
	if _, err := l.writer.Write(logEntry); err != nil {
		fmt.Fprintf(os.Stderr, "logger: failed to write: %v\n", err)
	}
}

// checkRotate checks file size and triggers rotation if needed.
// It is ONLY called by the `run` goroutine.
func (l *Logger) checkRotate() error {
	stat, err := l.file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() < l.maxSize {
		return nil // No rotation needed
	}
	return l.rotate()
}

// rotate performs the log file rotation.
// It is ONLY called by the `run` goroutine.
func (l *Logger) rotate() error {
	// 1. Flush and close current file
	if err := l.writer.Flush(); err != nil {
		l.file.Close() // Attempt to close even if flush fails
		return fmt.Errorf("failed to flush old log: %w", err)
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("failed to close old log: %w", err)
	}

	// 2. Rename old backups
	baseFilename := l.filename
	ext := filepath.Ext(baseFilename)
	prefix := strings.TrimSuffix(baseFilename, ext)

	// Delete the oldest backup if it exists
	oldestBackup := fmt.Sprintf("%s.%d%s", prefix, l.maxBackups, ext)
	os.Remove(oldestBackup) // Ignore error if it doesn't exist

	// Shift remaining backups
	for i := l.maxBackups - 1; i > 0; i-- {
		src := fmt.Sprintf("%s.%d%s", prefix, i, ext)
		dst := fmt.Sprintf("%s.%d%s", prefix, i+1, ext)

		if _, err := os.Stat(src); err == nil {
			os.Rename(src, dst) // Ignore error
		}
	}

	// 3. Rename current log to .1
	backup1 := fmt.Sprintf("%s.1%s", prefix, ext)
	if err := os.Rename(l.filename, backup1); err != nil {
		fmt.Fprintf(os.Stderr, "logger: failed to rename log: %v\n", err)
	}

	// 4. Open a new log file
	newFile, err := os.OpenFile(l.filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open new log file: %w", err)
	}

	// 5. Update logger state
	l.file = newFile
	l.writer = bufio.NewWriter(newFile)

	return nil
}

// Close gracefully shuts down the logger, flushing all pending logs.
func (l *Logger) Close() error {
	// Signal the `run` goroutine to stop
	close(l.doneChan)

	// Wait for the goroutine to finish draining the channel and closing the file
	l.wg.Wait()
	return nil
}

//
