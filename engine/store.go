package engine

// Store is the SQLite file behind an Engine. The Engine reads from it only when it opens and when an
// event touches a window it no longer holds in memory, and writes to it at checkpoints.
type Store interface {
	// Exec runs SQL that takes no parameters and returns no rows, such as the compiled schema.
	Exec(sql string) error
	// Query runs one statement with positional parameters and calls fn for each row.
	// Parameters and row values are nil, int64, float64 or string.
	Query(sql string, args []any, fn func(row []any) error) error
	// Apply writes one checkpoint in a single transaction: every block in order, then each
	// distill statement with :now bound to Now. Either all of it is in the file afterwards or none.
	Apply(b *Batch) error
	// Log is the natural logarithm that the file's SQLite uses for ln(), so that baselines the
	// Engine computes match the ones the compiled triggers compute, to the last bit.
	Log(x float64) float64
}

// Batch is one checkpoint.
type Batch struct {
	Blocks  []*Block
	Distill []string
	Now     int64
}

// Block is one statement run once per row. Each row binds the parameters in the order of Types:
// 'i' takes the next of Ints, 'f' the next of Reals and 't' the next text, given by Offs and Lens
// into Text.
type Block struct {
	SQL   string
	Types []byte
	Ints  []int64
	Reals []float64
	Text  []byte
	Offs  []int32
	Lens  []int32
	N     int
}

func newBlock(sql string, types []byte) *Block { return &Block{SQL: sql, Types: types} }

// NewBlock starts a block for a State: types has one letter per parameter, i, f or t.
func NewBlock(sql, types string) *Block { return newBlock(sql, []byte(types)) }

// AddInt, AddReal and AddText add the next parameter of a row; EndRow ends the row.
func (b *Block) AddInt(v int64)    { b.int(v) }
func (b *Block) AddReal(v float64) { b.real(v) }
func (b *Block) AddText(s string)  { b.text(s) }
func (b *Block) EndRow()           { b.row() }

func (b *Block) int(v int64)    { b.Ints = append(b.Ints, v) }
func (b *Block) real(v float64) { b.Reals = append(b.Reals, v) }
func (b *Block) text(s string) {
	b.Offs = append(b.Offs, int32(len(b.Text)))
	b.Lens = append(b.Lens, int32(len(s)))
	b.Text = append(b.Text, s...)
}
func (b *Block) row() { b.N++ }

// Value returns parameter p of row r, for stores that bind values one at a time.
// It walks the block from the start, so it suits tests and small blocks.
func (b *Block) Value(r, p int) any {
	var ii, fi, ti int
	for row := 0; row <= r; row++ {
		for c, t := range b.Types {
			if row == r && c == p {
				switch t {
				case 'i':
					return b.Ints[ii]
				case 'f':
					return b.Reals[fi]
				case 't':
					return string(b.Text[b.Offs[ti] : b.Offs[ti]+b.Lens[ti]])
				}
				return nil
			}
			switch t {
			case 'i':
				ii++
			case 'f':
				fi++
			case 't':
				ti++
			}
		}
	}
	return nil
}
