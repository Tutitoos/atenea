package desktop

import "os"

// UnderLaunchd reports whether this process was started by launchd rather than
// by a shell, which on macOS is the same question as "is Atenea the process
// the system will attribute a device permission to".
//
// It matters because of a measured behavior rather than a documented one. On
// macOS 26.6, TCC attributes a grant to the RESPONSIBLE ANCESTOR and not to
// the process asking: a signed binary whose own identifier was never
// authorized reported full Accessibility and Screen Recording merely for
// having been launched from a terminal that held them. The grant flows down
// every os/exec descendant from that ancestor.
//
// So `atenea` run from a shell borrows the shell's permissions, and anything
// it spawns borrows them too. That is not a permission anybody granted Atenea,
// it cannot be revoked by revoking Atenea's, and Atenea's own settings cannot
// switch it off. Under launchd, `atenea` is responsible for itself and the
// grant is genuinely its own.
//
// Two signals rather than one because each is weak alone. XPC_SERVICE_NAME is
// set by launchd for the jobs it manages, but a shell can export anything;
// a parent of pid 1 is what a launchd job has, but so does anything whose
// parent exited and left it reparented.
func UnderLaunchd() bool {
	if name, ok := os.LookupEnv("XPC_SERVICE_NAME"); ok && name != "" && name != "0" {
		return true
	}
	return os.Getppid() == 1
}
