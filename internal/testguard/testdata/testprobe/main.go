package main

import (
	"fmt"

	"github.com/hea3ven/orpheus/internal/testguard"
)

func main() {
	fmt.Println(testguard.IsTestProcess())
}
