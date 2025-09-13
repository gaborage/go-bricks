// Package main demonstrates the enhanced handler system usage.
package main

import (
	"log"

	"github.com/gaborage/go-bricks/app"
)

func main() {
	// Create application
	application, err := app.New()
	if err != nil {
		log.Fatalf("Failed to create application: %v", err)
	}

	// Register the user module with enhanced handlers
	userModule := NewUserModule()
	if err := application.RegisterModule(userModule); err != nil {
		log.Fatalf("Failed to register user module: %v", err)
	}

	// Start the application
	log.Println("Starting application with enhanced handlers...")
	if err := application.Run(); err != nil {
		log.Fatalf("Application failed: %v", err)
	}
}
