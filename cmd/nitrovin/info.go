package main

import "flag"

func init() {
	register(command{
		name:     "info",
		synopsis: "",
		summary:  "Print the decoder version, data month, artifact hash, data source and elements (GET /v1/info).",
		setup: func(*flag.FlagSet) func(*env, []string) error {
			return func(e *env, args []string) error {
				if len(args) > 0 {
					return usageError("info takes no arguments")
				}
				d, err := e.decoder()
				if err != nil {
					return err
				}
				return writeObject(e, d.Info())
			}
		},
	})
}
