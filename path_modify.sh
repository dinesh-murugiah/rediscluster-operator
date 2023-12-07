#/bin/sh

find ./ -type f -exec awk '
    BEGIN { 
        old_str="github.com/ucloud"
        new_str="github.com/dinesh-murugiah"
    }
    { 
        while (match($0, old_str)) {
            start = RSTART
            end = RSTART + RLENGTH - 1

            printf("Found occurrence in file: %s\n", FILENAME)
            printf("Original line: %s\n", $0)
            printf("Replace \"%s\" with \"%s\"? (y/n): ", substr($0, start, end-start+1), new_str)
            getline answer < "/dev/tty"
            
            if (answer == "y") {
                $0 = substr($0, 1, start-1) new_str substr($0, end+1)
                printf("Replaced line: %s\n", $0)
            } else {
                break
            }
        }
    }
    { print > FILENAME }
' {} +


