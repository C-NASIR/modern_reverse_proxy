package runtime

type Pressure struct {
	store *Store
}

func NewPressure(store *Store) *Pressure {
	return &Pressure{store: store}
}

func (p *Pressure) UnderPressure() bool {
	if p == nil || p.store == nil {
		return false
	}
	return p.store.RetiredCount() >= p.store.maxRetiredSnapshots()
}
