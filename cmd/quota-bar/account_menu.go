package main

import "github.com/getlantern/systray"

const resetCreditSlots = 8

type menuItem struct {
	key  string
	item *systray.MenuItem
}

func itemKey(provider, suffix string) string {
	return provider + "_" + suffix
}

type accountMenuSpec struct {
	provider, key, label string
	windows              []string
}

type accountMenuBlock struct {
	header, errorRow, resets *systray.MenuItem
	rows, resetChildren      []*systray.MenuItem
}

func (b *accountMenuBlock) rootCount() int {
	n := 2 + len(b.rows)
	if b.resets != nil {
		n++
	}
	return n
}

type accountMenus struct {
	pools           map[string][]*accountMenuBlock
	pending         *accountMenuBlock
	pendingProvider string
	providers       []string
	items           []menuItem
	keys            []string
	errors          map[string]*systray.MenuItem
	resets          map[string]*systray.MenuItem
	resetChildren   map[string][]*systray.MenuItem
}

func newAccountMenus() *accountMenus {
	return &accountMenus{
		pools:  map[string][]*accountMenuBlock{},
		errors: map[string]*systray.MenuItem{}, resets: map[string]*systray.MenuItem{},
		resetChildren: map[string][]*systray.MenuItem{},
	}
}

func (m *accountMenus) insertionIndex(provider string) int {
	position := 0
	for _, p := range []string{"claude", "codex"} {
		for _, b := range m.pools[p] {
			position += b.rootCount()
		}
		if p == provider {
			break
		}
	}
	return position
}

func (m *accountMenus) prepare(entries []accountMenuSpec) error {
	if m.pending != nil {
		if err := m.placePending(); err != nil {
			return err
		}
	}
	indices := map[string]int{}
	for _, a := range entries {
		index := indices[a.provider]
		indices[a.provider]++
		if index == len(m.pools[a.provider]) {
			if err := beginMenuBlock(); err != nil {
				return err
			}
			b := &accountMenuBlock{header: systray.AddMenuItem("", ""), errorRow: systray.AddMenuItem("", "")}
			b.header.Disable()
			b.header.Hide()
			b.errorRow.Disable()
			b.errorRow.Hide()
			for range a.windows {
				row := systray.AddMenuItemCheckbox("-", "", false)
				row.Hide()
				b.rows = append(b.rows, row)
			}
			if a.provider == "codex" {
				b.resets = systray.AddMenuItem("Reset credits", "")
				b.resets.Hide()
				for range resetCreditSlots {
					child := b.resets.AddSubMenuItem("", "")
					child.Hide()
					b.resetChildren = append(b.resetChildren, child)
				}
			}
			m.pending, m.pendingProvider = b, a.provider
			if err := m.placePending(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *accountMenus) placePending() error {
	if err := endMenuBlockAt(m.insertionIndex(m.pendingProvider), m.pending.rootCount()); err != nil {
		return err
	}
	m.pools[m.pendingProvider] = append(m.pools[m.pendingProvider], m.pending)
	m.pending, m.pendingProvider = nil, ""
	return nil
}

func (m *accountMenus) reconcile(entries []accountMenuSpec, selected func(string) bool) error {
	if err := m.prepare(entries); err != nil {
		return err
	}
	m.providers, m.items, m.keys = nil, nil, nil
	clear(m.errors)
	clear(m.resets)
	clear(m.resetChildren)
	for _, blocks := range m.pools {
		for _, b := range blocks {
			b.header.Hide()
			b.errorRow.Hide()
			for _, row := range b.rows {
				row.Hide()
			}
			if b.resets != nil {
				b.resets.Hide()
			}
		}
	}
	indices := map[string]int{}
	for _, a := range entries {
		index := indices[a.provider]
		indices[a.provider]++
		b := m.pools[a.provider][index]
		b.header.SetTitle("── " + a.label + " ──")
		b.header.Show()
		m.providers = append(m.providers, a.key)
		m.errors[a.key] = b.errorRow
		for i, wk := range a.windows {
			item := menuItem{itemKey(a.key, wk), b.rows[i]}
			if selected(item.key) {
				item.item.Check()
			} else {
				item.item.Uncheck()
			}
			item.item.Hide()
			m.items = append(m.items, item)
			m.keys = append(m.keys, item.key)
		}
		if b.resets != nil {
			m.resets[a.key], m.resetChildren[a.key] = b.resets, b.resetChildren
		}
	}
	return nil
}
