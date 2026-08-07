//go:build !unix

package pidlock

// alive has no signal-0 probe outside unix. Assuming alive is the safe
// direction: it costs an operator a manual retry on a genuinely dead lock,
// where the other guess would risk two holders at once.
func alive(int) bool { return true }
