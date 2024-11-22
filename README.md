# Regexpand

## Purpose

`regexpand` is a Golang library that provides information about the strings a regular expression matches.

It can be used to guess type information from regular expessions, turn field validation into dropdowns...

## Usage

See godoc for more.

### `Expand()`
```golang
const maxOutputLen = 100
res, _ := regexpand.Expand("(?i:p)u[m]p(kin|)s?", maxOutputLen)
fmt.Printf("%+v\n", res)
// Output:
// [Pump Pumpkin Pumpkins Pumps pump pumpkin pumpkins pumps]
```

### `ASCIIRange()`

```golang
res, _ := regexpand.ASCIIRange(`\d+`)
fmt.Println(res)
// Output:
// [0-9]
```

## Legal notice
This is not an officially supported Google product. This project is not
eligible for the [Google Open Source Software Vulnerability Rewards
Program](https://bughunters.google.com/open-source-security).

