package fixturego

// Repository is the small Go contract used by the fixed context corpus.
type Repository interface {
	Get(id string) (string, bool)
}

// MemoryRepository is a concrete implementation used by the semantic fixture.
type MemoryRepository struct {
	items map[string]string
}

// Get implements Repository without depending on any external service.
func (m MemoryRepository) Get(id string) (string, bool) {
	value, ok := m.items[id]
	return value, ok
}
