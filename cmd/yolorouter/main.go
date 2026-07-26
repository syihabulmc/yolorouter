package main

import (
	"context"
	"os"

	// Embed the IANA timezone database so time.LoadLocation works on all
	// platforms (Windows, minimal Docker) without a system zoneinfo.
	_ "time/tzdata"
)

func main() {
	code, err := dispatch(context.Background(), os.Args[1:])
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
	}
	os.Exit(code)
}
