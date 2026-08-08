// Package render produces the non-HTML representations of the countdown: the
// calendar file and the link-preview image.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ICSMaxLineOctets is the folding limit from RFC 5545. Lines longer than this must
// be split, or strict clients reject the whole file.
const ICSMaxLineOctets = 75

// CalendarEvent is one entry in the exported calendar.
type CalendarEvent struct {
	// UID must be stable across exports so that re-importing updates the existing
	// entry rather than creating a duplicate every time.
	UID         string
	Summary     string
	Description string
	Start       time.Time
	// AllDay renders the entry as a whole-day event, which is what a date with no
	// meaningful time of day should be.
	AllDay bool
}

// Calendar renders events as an iCalendar file.
//
// Everything is emitted in UTC, which every client understands without needing the
// timezone definitions that a VTIMEZONE block would otherwise have to carry.
func Calendar(name string, events []CalendarEvent, now time.Time) []byte {
	var b strings.Builder

	write := func(line string) {
		b.WriteString(foldLine(line))
		b.WriteString("\r\n")
	}

	write("BEGIN:VCALENDAR")
	write("VERSION:2.0")
	write("PRODID:-//waitformeet//EN")
	write("CALSCALE:GREGORIAN")
	write("METHOD:PUBLISH")
	write("X-WR-CALNAME:" + escapeText(name))

	for _, e := range events {
		write("BEGIN:VEVENT")
		write("UID:" + e.uid())
		write("DTSTAMP:" + now.UTC().Format("20060102T150405Z"))

		if e.AllDay {
			// A date-valued DTSTART with a DTEND on the following day is how a
			// single all-day entry is expressed.
			write("DTSTART;VALUE=DATE:" + e.Start.UTC().Format("20060102"))
			write("DTEND;VALUE=DATE:" + e.Start.UTC().AddDate(0, 0, 1).Format("20060102"))
		} else {
			write("DTSTART:" + e.Start.UTC().Format("20060102T150405Z"))
			write("DTEND:" + e.Start.UTC().Add(time.Hour).Format("20060102T150405Z"))
		}

		write("SUMMARY:" + escapeText(e.Summary))
		if e.Description != "" {
			write("DESCRIPTION:" + escapeText(e.Description))
		}
		write("END:VEVENT")
	}

	write("END:VCALENDAR")
	return []byte(b.String())
}

// uid returns the event's identifier, deriving a stable one when none was supplied.
func (e CalendarEvent) uid() string {
	if e.UID != "" {
		return e.UID
	}
	sum := sha256.Sum256([]byte(e.Summary + "|" + e.Start.UTC().Format(time.RFC3339)))
	return hex.EncodeToString(sum[:8]) + "@waitformeet"
}

// escapeText applies the RFC 5545 rules for text values. Backslash first, or the
// escapes introduced afterwards would themselves be escaped.
func escapeText(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		"\n", `\n`,
		"\r", "",
		";", `\;`,
		",", `\,`,
	).Replace(s)
}

// foldLine wraps a long line the way RFC 5545 requires: a CRLF followed by one
// space, which the reader removes again.
//
// The limit counts octets rather than characters, so a Cyrillic or Chinese summary
// must not be split in the middle of a rune; doing so would corrupt the text.
func foldLine(line string) string {
	if len(line) <= ICSMaxLineOctets {
		return line
	}

	var b strings.Builder
	// written counts the octets on the current physical line. A continuation line
	// starts at 1 because its leading space counts towards the limit.
	written := 0

	for _, r := range line {
		size := len(string(r))
		if written+size > ICSMaxLineOctets {
			b.WriteString("\r\n ")
			written = 1
		}
		b.WriteRune(r)
		written += size
	}
	return b.String()
}

// ContentDisposition returns the header value that makes a browser download the
// calendar under a sensible name.
func ContentDisposition(name string) string {
	safe := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`"\/:*?<>|`, r) || r < 0x20 {
			return '-'
		}
		return r
	}, name)
	return fmt.Sprintf(`attachment; filename="%s.ics"`, safe)
}
