my log library

```go
package example

import (
	"fmt"
	"os"
	"sync"
)

func main() {
	// Cleanup old logs for a clean run
	os.Remove("app.log")
	os.Remove("app.log.1")
	os.Remove("structured.json")
	os.Remove("structured.json.1")
	os.Remove("rotate.log")
	os.Remove("rotate.log.1")
	os.Remove("rotate.log.2")
	os.Remove("concurrent.log")
	os.Remove("concurrent.log.1")

	fmt.Println("Starting logger example...")

	// --- Plain Text Logger ---
	plainLogger, err := NewLogger(Config{
		Filename:   "app.log",
		Structured: false,
		Level:      InfoLevel,
		MaxSizeMB:  10,
		MaxBackups: 1,
	})
	if err != nil {
		panic(err)
	}

	plainLogger.Info("Plain logger started.")
	plainLogger.Debug("This debug message will be skipped.")
	plainLogger.Warnf("Warning: %s", "disk is 75% full")
	plainLogger.Close() // Close waits for logs to be written
	fmt.Println("Wrote to app.log.")

	// --- Rotation Test Logger (tiny size) ---
	rotLogger, err := NewLogger(Config{
		Filename:   "rotate.log",
		Structured: false,
		Level:      DebugLevel,
		MaxSizeMB:  1, // Will use 1MB, but we override size below
		MaxBackups: 2,
	})
	if err != nil {
		panic(err)
	}
	// Manually set a very small size for testing
	rotLogger.maxSize = 512 // 512 bytes

	fmt.Println("Running rotation test with 512 byte limit...")
	for i := 0; i < 20; i++ {
		rotLogger.Infof("This is log message number %d. It adds some bytes.", i)
	}
	rotLogger.Close()
	fmt.Println("Rotation test finished. Check 'rotate.log', 'rotate.log.1', 'rotate.log.2'.")

	// --- Structured JSON Logger ---
	jsonLogger, err := NewLogger(Config{
		Filename:   "structured.json",
		Structured: true,
		Level:      DebugLevel,
		MaxSizeMB:  10,
		MaxBackups: 3,
	})
	if err != nil {
		panic(err)
	}

	jsonLogger.Info("JSON logger started.")
	jsonLogger.Debug("This is a structured debug message.")
	jsonLogger.Errorf("An error occurred: %s", "file not found")
	jsonLogger.Close()
	fmt.Println("Wrote to structured.json")

	// --- Concurrency Test ---
	fmt.Println("Starting concurrency test...")
	concurrentLogger, err := NewLogger(Config{
		Filename:   "concurrent.log",
		Structured: false,
		Level:      DebugLevel,
		MaxSizeMB:  1,
		MaxBackups: 1,
		BufferSize: 2048, // Large buffer
	})
	if err != nil {
		panic(err)
	}
	concurrentLogger.maxSize = 2048 // 2 KB limit for rotation

	var wg sync.WaitGroup
	numGoroutines := 10
	logsPerGoroutine := 100

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < logsPerGoroutine; j++ {
				concurrentLogger.Infof("Log from goroutine %d, message %d", id, j)
			}
		}(i)
	}

	wg.Wait()                // Wait for all app goroutines to finish *sending*
	concurrentLogger.Close() // Wait for logger goroutine to finish *writing*
	fmt.Println("Concurrency test finished.")
	fmt.Println("Check concurrent.log and concurrent.log.1 for output.")
}
```