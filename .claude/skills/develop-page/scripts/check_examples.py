#!/usr/bin/env python3
"""Check that a wording-only rewrite preserves the published examples and language tabs."""
import argparse
from collections import Counter
from dataclasses import dataclass
import hashlib
from pathlib import Path
import re
import sys


@dataclass(frozen=True)
class Example:
    section: str
    tab: str
    language: str
    body: str


def heading_key(value):
    anchor = re.search(r'\s+(?:\[#([^]]+)\]|\{#([^}]+)\})\s*$', value)
    if anchor:
        return anchor[1] or anchor[2]
    return re.sub(r'[^a-z0-9]+', '-', value.lower()).strip('-')


def read_page(path):
    text = Path(path).read_text()
    examples, tabs = Counter(), Counter()
    section, tab = 'preamble', 'shared'
    marker, language, body, started = '', '', [], 0
    for number, line in enumerate(text.splitlines(), 1):
        if marker:
            if re.fullmatch(r'\s*' + re.escape(marker[0]) + '{' + str(len(marker)) + r',}\s*', line):
                examples[Example(section, tab, language, '\n'.join(body).rstrip('\n'))] += 1
                marker, language, body = '', '', []
            else:
                body.append(line)
            continue
        if re.match(r'^\s*::include\[', line):
            raise ValueError(f'{path}:{number}: use the built Markdown with includes expanded')
        fence = re.match(r'^ {0,3}(`{3,}|~{3,})([^\n]*)$', line)
        if fence:
            marker, info, started = fence[1], fence[2].strip(), number
            language = info.split()[0] if info else 'text'
            continue
        if line.startswith('## '):
            section, tab = heading_key(line[3:]), 'shared'
        elif line.startswith('### '):
            label = re.sub(r'\s+\[#.*?\]\s*$', '', line[4:]).strip()
            if label in ('TypeScript', 'Python'):
                tab = label
                tabs[(section, tab)] += 1
            else:
                tab = 'shared'
    if marker:
        raise ValueError(f'{path}:{started}: unclosed code fence')
    return examples, tabs


def describe(example, count):
    digest = hashlib.sha256(example.body.encode()).hexdigest()[:10]
    return f'{example.section} / {example.tab} / {example.language}: {count} block(s), content {digest}'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('before', help='Published baseline Markdown with includes expanded')
    parser.add_argument('after', help='Rewritten published Markdown with includes expanded')
    args = parser.parse_args()
    try:
        before, old_tabs = read_page(args.before)
        after, new_tabs = read_page(args.after)
    except (OSError, ValueError) as error:
        print(error, file=sys.stderr)
        return 2
    missing, missing_tabs = before - after, old_tabs - new_tabs
    if missing or missing_tabs:
        print('FAIL: the wording rewrite removed, changed or moved an original example or language tab.')
        for example, count in missing.items():
            print('  ' + describe(example, count))
        for (section, tab), count in missing_tabs.items():
            print(f'  {section}: {count} missing {tab} tab(s)')
        return 1
    print(f'PASS: all {sum(before.values())} original code blocks and {sum(old_tabs.values())} language tabs are preserved.')
    added = after - before
    old_content = {(example.language, example.body) for example in before}
    repeated = sum(count for example, count in added.items() if (example.language, example.body) in old_content)
    if added:
        print(f'Added {sum(added.values())} block(s); {repeated} repeat existing code. Review whether they are needed.')
    print('This check covers code and tabs. Review prose coverage, setup, qualifiers and links separately.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
