package eventwire

import (
	"errors"
	"sort"
	"time"
)

type Count struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type Query struct {
	Source   Source
	Type     string
	Page     int
	PageSize int
}

type Page struct {
	Status       Status   `json:"status"`
	Retained     int      `json:"retained"`
	Matching     int      `json:"matching"`
	Page         int      `json:"page"`
	PageSize     int      `json:"pageSize"`
	PageCount    int      `json:"pageCount"`
	Records      []Record `json:"records"`
	SourceCounts []Count  `json:"sourceCounts"`
	TypeCounts   []Count  `json:"typeCounts"`
	HourCounts   []Count  `json:"hourCounts"`
}

func (j *Journal) Query(query Query) (Page, error) {
	if query.Page < 1 {
		return Page{}, errors.New("event wire: page must be positive")
	}
	if query.PageSize < 1 {
		return Page{}, errors.New("event wire: page size must be positive")
	}

	_, _, _, retained := j.Snapshot()
	status := j.Status()
	matching := make([]Record, 0, len(retained))
	filter := Filter{Source: query.Source, Type: query.Type}
	for i := len(retained) - 1; i >= 0; i-- {
		if filter.Matches(retained[i].Event) {
			matching = append(matching, retained[i])
		}
	}

	pageCount := 0
	if len(matching) > 0 {
		pageCount = (len(matching) + query.PageSize - 1) / query.PageSize
	}
	start := len(matching)
	if query.Page <= pageCount {
		start = (query.Page - 1) * query.PageSize
	}
	end := min(start+query.PageSize, len(matching))

	return Page{
		Status:       status,
		Retained:     len(retained),
		Matching:     len(matching),
		Page:         query.Page,
		PageSize:     query.PageSize,
		PageCount:    pageCount,
		Records:      cloneRecords(matching[start:end]),
		SourceCounts: countBy(retained, func(record Record) string { return string(record.Event.Source) }),
		TypeCounts:   countBy(retained, func(record Record) string { return record.Event.Type }),
		HourCounts:   countHours(retained),
	}, nil
}

func (j *Journal) Record(sequence uint64) (Record, bool) {
	_, _, _, records := j.Snapshot()
	for _, record := range records {
		if record.Sequence == sequence {
			return record, true
		}
	}
	return Record{}, false
}

func countBy(records []Record, label func(Record) string) []Count {
	counts := make(map[string]int)
	for _, record := range records {
		counts[label(record)]++
	}
	result := make([]Count, 0, len(counts))
	for value, count := range counts {
		result = append(result, Count{Label: value, Count: count})
	}
	sort.Slice(result, func(i, k int) bool {
		if result[i].Count == result[k].Count {
			return result[i].Label < result[k].Label
		}
		return result[i].Count > result[k].Count
	})
	return result
}

func countHours(records []Record) []Count {
	counts := make(map[time.Time]int)
	for _, record := range records {
		counts[record.Event.ReceivedAt.UTC().Truncate(time.Hour)]++
	}
	hours := make([]time.Time, 0, len(counts))
	for hour := range counts {
		hours = append(hours, hour)
	}
	sort.Slice(hours, func(i, k int) bool { return hours[i].Before(hours[k]) })
	if len(hours) > 12 {
		hours = hours[len(hours)-12:]
	}
	result := make([]Count, 0, len(hours))
	for _, hour := range hours {
		result = append(result, Count{Label: hour.Format("Jan 2 15:00"), Count: counts[hour]})
	}
	return result
}
