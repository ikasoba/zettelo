package core

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/adrg/frontmatter"
	bolt "go.etcd.io/bbolt"
)

type Zettelo struct {
	HomePath string
	db       *bolt.DB
}

func New(home string) (*Zettelo, error) {
	err := os.MkdirAll(home, 0755)
	if err != nil && err != os.ErrExist {
		return nil, err
	}

	db, err := bolt.Open(filepath.Join(home, "index.db"), 0600, nil)
	if err != nil {
		return nil, err
	}

	return &Zettelo{
		home,
		db,
	}, nil
}

func (z *Zettelo) Close() {
	z.db.Close()
}

func (z *Zettelo) GetAttribute(name string) (*NoteAttribute, error) {
	var res *NoteAttribute = nil

	err := z.db.View(func(tx *bolt.Tx) error {
		attrs, err := tx.CreateBucketIfNotExists([]byte("attrs"))
		if err != nil {
			return err
		}

		data := attrs.Get([]byte(name))
		if data == nil {
			return nil
		}

		var attr NoteAttribute

		if err := json.Unmarshal(data, &attr); err != nil {
			return err
		}

		res = &attr

		return nil
	})

	if err != nil {
		return nil, err
	}

	return res, nil
}

func (z *Zettelo) ReadNote(name string, w io.Writer) error {
	err := os.MkdirAll(filepath.Join(z.HomePath, "notes"), 0755)
	if err != nil && err != os.ErrExist {
		return err
	}

	p := filepath.Join(z.HomePath, "notes", PathEscape(name)+".md")

	f, err := os.OpenFile(p, os.O_RDONLY, 0755)
	if err != nil {
		return err
	}

	defer f.Close()

	_, err = f.WriteTo(w)

	return err
}

func (z *Zettelo) PutNote(name string, r io.Reader) error {
	err := os.MkdirAll(filepath.Join(z.HomePath, "notes"), 0755)
	if err != nil && err != os.ErrExist {
		return err
	}

	p := filepath.Join(z.HomePath, "notes", PathEscape(name)+".md")

	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}

	r = io.TeeReader(r, f)

	defer f.Close()

	var attr NoteAttribute

	if _, err := frontmatter.Parse(r, &attr); err != nil {
		return err
	}

	var previousAttr *NoteAttribute = nil

	err = z.db.Update(func(tx *bolt.Tx) error {
		attrs, err := tx.CreateBucketIfNotExists([]byte("attrs"))
		if err != nil {
			return err
		}

		previous_data := attrs.Get([]byte(name))
		if previous_data != nil {
			var prevAttr NoteAttribute
			if err := json.Unmarshal(previous_data, &prevAttr); err != nil {
				return err
			}

			previousAttr = &prevAttr
		}

		data, err := json.Marshal(attr)
		if err != nil {
			return err
		}

		return attrs.Put([]byte(name), data)
	})

	if err != nil {
		return err
	}

	tags := map[string]struct{}{}

	for _, v := range attr.Tags {
		tags[v] = struct{}{}
	}

	prevTags := map[string]struct{}{}

	if previousAttr != nil {
		for _, v := range previousAttr.Tags {
			prevTags[v] = struct{}{}
		}
	}

	modifiedTags := map[string]struct{}{}

	removedTags := map[string]struct{}{}

	for k := range prevTags {
		if _, ok := tags[k]; !ok {
			removedTags[k] = struct{}{}
			modifiedTags[k] = struct{}{}
		}
	}

	addedTags := map[string]struct{}{}

	for k := range tags {
		if _, ok := prevTags[k]; !ok {
			addedTags[k] = struct{}{}
			modifiedTags[k] = struct{}{}
		}
	}

	err = z.db.Update(func(tx *bolt.Tx) error {
		tagsStats, err := tx.CreateBucketIfNotExists([]byte("tags_stats"))
		if err != nil {
			return err
		}

		buckets := map[string]*bolt.Bucket{}
		for k := range modifiedTags {
			b, err := tx.CreateBucketIfNotExists(append([]byte("tags/"), []byte(url.PathEscape(k))...))
			if err != nil {
				return err
			}

			buckets[k] = b
		}

		updatedStats := map[string]int64{}

		for k := range removedTags {
			b := buckets[k]

			if err := b.Delete([]byte(name)); err != nil {
				return err
			}

			updatedStats[k]--
		}

		for k := range addedTags {
			b := buckets[k]

			if err := b.Put([]byte(name), []byte{0}); err != nil {
				return err
			}

			updatedStats[k]++
		}

		for k, d := range updatedStats {
			count := int64(0)

			raw := tagsStats.Get([]byte(k))
			if raw != nil {
				count = int64(binary.LittleEndian.Uint64(raw))
			}

			count += d

			if count < 0 {
				count = 0
			}

			if count == 0 {
				if err := tagsStats.Delete([]byte(k)); err != nil {
					return err
				}
			} else {
				buf := make([]byte, 8)
				binary.LittleEndian.PutUint64(buf, uint64(count))

				if err := tagsStats.Put([]byte(k), buf); err != nil {
					return err
				}
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

func (z *Zettelo) Filter(query [][]string, seeks map[string]string, max int) ([]string, map[string]string, error) {
	tags := map[string]int{}

	for _, v := range query {
		for _, k := range v {
			tags[k]++
		}
	}

	primaries := []string{}
	{
		temp := map[string]struct{}{}

		for _, v := range query {
			maxKey := ""
			maxCount := -1

			for _, k := range v {
				count := tags[k]

				if maxCount <= count {
					maxKey = k
					maxCount = count
				}
			}

			if maxCount >= 0 {
				temp[maxKey] = struct{}{}
			}
		}

		for k := range temp {
			primaries = append(primaries, k)
		}
	}

	lastPositions := map[string]string{}

	results := []string{}

	err := z.db.Update(func(tx *bolt.Tx) error {
		buckets := map[string]*bolt.Bucket{}
		for k := range tags {
			b, err := tx.CreateBucketIfNotExists(append([]byte("tags/"), []byte(url.PathEscape(k))...))
			if err != nil {
				return err
			}

			buckets[k] = b
		}

		cursors := map[string]*bolt.Cursor{}
		for _, k := range primaries {
			cursors[k] = buckets[k].Cursor()
		}

		for t, k := range seeks {
			if c, ok := cursors[t]; ok {
				c.Seek([]byte(k))
			}
		}

		for i := 0; i < max && len(cursors) > 0; i++ {
			items := map[string]struct{}{}

			for k, c := range cursors {
				name := []byte{}
				if i == 0 {
					if v, ok := seeks[k]; ok {
						name, _ = c.Seek([]byte(v))
					} else {
						name, _ = c.First()
					}
				} else {
					name, _ = c.Next()
				}

				if name == nil {
					delete(cursors, k)
				}

				lastPositions[k] = string(name)

				items[string(name)] = struct{}{}
			}

			temp := map[string]struct{}{}

			for name := range items {
				for _, v := range query {
					isAll := true

					for _, k := range v {
						b := buckets[k]

						if b.Get([]byte(name)) == nil {
							isAll = false
							break
						}
					}

					if _, ok := temp[name]; !ok && isAll {
						results = append(results, name)
						temp[name] = struct{}{}
					}
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	return results, lastPositions, nil
}

func (z *Zettelo) GetTagsStats(seek string, limit int) (map[string]int64, string, error) {
	var lastSeek string

	stats := map[string]int64{}

	err := z.db.Update(func(tx *bolt.Tx) error {
		tagsStats, err := tx.CreateBucketIfNotExists([]byte("tags_stats"))
		if err != nil {
			return err
		}

		c := tagsStats.Cursor()

		for i := 0; i < limit; i++ {
			var k []byte
			var v []byte

			if i == 0 {
				if len(seek) > 0 {
					k, v = c.Seek([]byte(seek))
				} else {
					k, v = c.First()
				}
			} else {
				k, v = c.Next()
			}

			if k == nil {
				break
			}

			stats[string(k)] = int64(binary.LittleEndian.Uint64(v))

			lastSeek = string(k)
		}

		return nil
	})

	if err != nil {
		return nil, "", err
	}

	return stats, lastSeek, nil
}
