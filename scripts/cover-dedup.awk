# Collapse duplicate blocks in ONE Go coverage profile (stdin or a file).
#
#   awk -f scripts/cover-dedup.awk coverage.out > coverage.out.dedup
#
# `go test -coverpkg` writes every block once per test binary, and concatenated
# profiles repeat the blocks they share. This prints the mode line once, then
# each block once in first-appearance order with its counts merged the way
# go tool cover merges them: summed in count/atomic mode, OR'd (max) in set
# mode. A block's key is its `file:L.C,L.C` range (NumStmt and count stripped),
# so a file name may hold a space. Counts print as integers (exact below 2^53).
#
# Exits 2 with a message on stderr, before printing anything, on: empty input;
# a first line other than `mode: set|count|atomic`; no blocks; a blank line; a
# CR; a second mode line; a line not shaped `file:L.C,L.C NumStmt Count`; or one
# range carrying two NumStmt values. POSIX awk only (BSD awk, mawk, gawk).

function die(msg) {
  print "cover-dedup: " msg > "/dev/stderr"
  failed = 1
  exit 2
}

function bad(what) { die("line " NR ": " what) }

index($0, "\r") { bad("carriage return (CRLF line endings?)") }

NR == 1 {
  if ($0 !~ /^mode: (set|count|atomic)$/) bad("first line is not mode: set|count|atomic")
  mode = substr($0, 7)
  next
}

/^[ \t]*$/ { bad("blank line") }
/^mode:/ { bad("second mode line") }
$0 !~ /^.+:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+ [0-9]+ [0-9]+$/ { bad("malformed block: " $0) }

{
  match($0, / [0-9]+ [0-9]+$/)
  r = substr($0, 1, RSTART - 1)
  split(substr($0, RSTART + 1), f, " ")
  if (!(r in stmts)) {
    order[++n] = r
    stmts[r] = f[1] ""
    counts[r] = 0
  } else if (stmts[r] != f[1] "") {
    bad("NumStmt " f[1] " differs from " stmts[r] " for " r)
  }
  if (mode != "set") counts[r] += f[2]
  else if (f[2] + 0 > counts[r]) counts[r] = f[2] + 0
}

END {
  if (failed) exit 2
  if (NR == 0) die("empty input")
  if (n == 0) die("no coverage blocks after the mode line")
  print "mode: " mode
  for (i = 1; i <= n; i++) printf "%s %s %.0f\n", order[i], stmts[order[i]], counts[order[i]]
}
