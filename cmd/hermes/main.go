// Command hermes runs the HERMES voice assistant server. All of the code
// lives in internal/hermes; this is only the entry point.
package main

import "bt-chatbot/internal/hermes"

func main() {
	hermes.Run()
}
