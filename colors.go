// Copyright 2022 Twitter, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import "charm.land/lipgloss/v2"

// ANSI palette indexes.
const (
	black = iota
	red8
	green8
	yellow8
	blue8
	magenta8
	cyan8
	white8
	gray8
	brightRed
	brightGreen
	brightYellow
	brightBlue
	brightMagenta
	brightCyan
	brightWhite
)

// noColor marks an unset foreground/background.
const noColor = -1

func style(fg, bg int) func(string) string {
	s := lipgloss.NewStyle()
	if fg != noColor {
		s = s.Foreground(lipgloss.ANSIColor(fg))
	}
	if bg != noColor {
		s = s.Background(lipgloss.ANSIColor(bg))
	}
	return func(v string) string { return s.Render(v) }
}

// colors are the per-context prefix styles. Render always emits ANSI; the
// colorprofile writers in main downsample or strip it for non-TTY output.
var colors = []func(string) string{
	// foreground only
	style(red8, noColor),
	style(blue8, noColor),
	style(green8, noColor),
	style(yellow8, black),
	style(gray8, noColor),
	style(magenta8, noColor),
	style(cyan8, noColor),
	style(brightRed, noColor),

	style(brightBlue, noColor),
	style(brightGreen, noColor),
	style(brightMagenta, noColor),
	style(brightYellow, black),
	style(brightCyan, noColor),

	// inverse
	style(white8, red8),
	style(white8, blue8),
	style(black, cyan8),
	style(black, green8),
	style(brightWhite, magenta8),
	style(black, yellow8),
	style(white8, gray8),
	style(white8, brightRed),
	style(white8, brightBlue),
	style(black, brightCyan),
	style(black, brightGreen),
	style(black, brightMagenta),
	style(black, brightYellow),

	// mixes+inverses
	style(yellow8, red8),
	style(red8, yellow8),
	style(yellow8, blue8),
	style(blue8, yellow8),
	style(brightWhite, black),
	style(black, brightWhite),
}
