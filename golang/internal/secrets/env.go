package secrets

import "os"

func lookupEnvironment(name string) (string, bool) { return os.LookupEnv(name) }
