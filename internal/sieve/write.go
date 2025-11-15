package sieve

import (
    "fmt"
    "io/ioutil"
    "os"
    "path/filepath"
)

func WriteScripts(scripts []SieveScript, dest string) error {
    if err := os.MkdirAll(dest, 0755); err != nil {
        return err
    }

    for _, s := range scripts {
        path := filepath.Join(dest, fmt.Sprintf("%s.sieve", s.Name))
        if err := ioutil.WriteFile(path, []byte(s.Content), 0644); err != nil {
            return err
        }
    }
    return nil
}
