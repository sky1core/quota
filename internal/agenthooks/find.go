package agenthooks

import (
	"fmt"
	"strings"
)

var findOptions = optionValues(
	optionGroup{optionNoValue, `-H -L -P -E -X -d -s -x -- -O0 -O1 -O2 -O3
		( ) ! -not -a -and -o -or , -true -false -print -print0 -ls -prune -quit
		-daystart -follow -xdev -mount -noleaf -ignore_readdir_race
		-noignore_readdir_race -empty -readable -writable -executable -nouser -nogroup
		-acl -sparse -xattr -delete -help --help -version --version`},
	optionGroup{optionRequiredValue, `-f -D -name -iname -path -ipath -wholename -iwholename
		-regex -iregex -regextype -type -xtype -size -user -group -uid -gid -perm -flags
		-inum -links -samefile -newer -anewer -cnewer -Bnewer -mnewer
		-neweraa -neweraB -newerac -neweram -newerat -newerBa -newerBB -newerBc -newerBm -newerBt
		-newerca -newercB -newercc -newercm -newerct -newerma -newermB -newermc -newermm -newermt
		-atime -ctime -mtime -Btime -amin -cmin -mmin -Bmin -used -maxdepth -mindepth
		-fstype -lname -ilname -printf -fprintf -fprint -fprint0 -fls -files0-from -xattrname`},
	optionGroup{optionOptionalNumber, `-depth`},
)

func parseFindCommands(input commandInput) ([]commandInput, error) {
	var commands []commandInput
	expression := false
	for i := 1; i < len(input.argv); i++ {
		arg := input.argv[i]
		if input.maySplitAt(i) || input.dynamicAt(i) && expression {
			return nil, fmt.Errorf("find expression cannot be determined")
		}
		if !expression && !strings.HasPrefix(arg, "-") && arg != "!" && arg != "(" {
			continue
		}
		switch arg {
		case "-exec", "-execdir", "-ok", "-okdir":
			expression = true
			start := i + 1
			i = start
			for i < len(input.argv) {
				if !input.dynamicAt(i) && (input.argv[i] == ";" ||
					input.argv[i] == "+" && i > start && input.argv[i-1] == "{}" && !input.dynamicAt(i-1) && (arg == "-exec" || arg == "-execdir")) {
					break
				}
				i++
			}
			if i == len(input.argv) || i == start {
				return nil, fmt.Errorf("find %s requires a command and terminator", arg)
			}
			nested := commandInput{argv: append([]string(nil), input.argv[start:i]...), dynamicArgs: make([]bool, i-start), splitArgs: make([]bool, i-start), literalPrefix: make([]bool, i-start)}
			for j, value := range nested.argv {
				nested.dynamicArgs[j] = input.dynamicAt(start + j)
				nested.splitArgs[j] = input.maySplitAt(start + j)
				nested.literalPrefix[j] = input.literalPrefixAt(start + j)
				if index := strings.Index(value, "{}"); index >= 0 {
					nested.argv[j] = value[:index]
					nested.dynamicArgs[j] = true
					nested.splitArgs[j] = input.argv[i] == "+" || nested.splitArgs[j]
					nested.literalPrefix[j] = index > 0
				}
			}
			commands = append(commands, nested)
		default:
			value, ok := findOptions[arg]
			if !ok || input.dynamicAt(i) {
				return nil, fmt.Errorf("unsupported find expression %q", arg)
			}
			if value == optionRequiredValue {
				i++
				if i == len(input.argv) || input.maySplitAt(i) {
					return nil, fmt.Errorf("find %s requires one argument", arg)
				}
				if arg == "-fprintf" {
					i++
					if i == len(input.argv) || input.maySplitAt(i) {
						return nil, fmt.Errorf("find -fprintf requires a format argument")
					}
				}
			}
			if value == optionOptionalNumber && i+1 < len(input.argv) && !input.dynamicAt(i+1) {
				number := input.argv[i+1]
				if strings.HasPrefix(number, "+") || strings.HasPrefix(number, "-") {
					number = number[1:]
				}
				if intArgRe.MatchString(number) {
					i++
				}
			}
			if arg != "-f" && arg != "-D" && arg != "--" && !strings.Contains(" -H -L -P -E -X -d -s -x -O0 -O1 -O2 -O3 ", " "+arg+" ") {
				expression = true
			}
		}
	}
	return commands, nil
}
