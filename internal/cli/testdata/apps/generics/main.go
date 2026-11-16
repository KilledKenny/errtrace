// Command generics is a demo of a generic collections and data-pipeline
// library: validation, slice pipelines, iterators, typed caches, a generic
// repository and generic error types.
package main

import (
	"fmt"
	"os"

	"example.com/generics/cache"
	"example.com/generics/model"
	"example.com/generics/repo"
	"example.com/generics/result"
	"example.com/generics/seq"
	"example.com/generics/validate"
)

func main() {
	steps := []func() error{runValidation, runPipeline, runCaching, runRepo, runFailures}
	for _, step := range steps {
		if err := step(); err != nil {
			fmt.Fprintln(os.Stderr, "generics:", err)
		}
	}
}

func runValidation() error {
	report(checkInferred(7))
	report(checkExplicit("ada"))
	report(checkEmail(&validate.Email{Addr: "ada@example.com"}))
	report(checkIface(validate.Name("bob")))
	report(checkIface(&validate.Email{Addr: "bob"}))
	report(validate.All[validate.UserID](1, 2, 3))
	report(validate.All(validate.Name("x"), validate.Name("")))
	if _, err := newEmail("carol@example.com"); err != nil {
		return err
	}
	return checkLimits(41, 7, 0.5)
}

func runPipeline() error {
	src := []string{"80", "443", ""}
	ports, err := parsePorts(src)
	report(err)
	_, err = lengths([]validate.Name{"ada", "bob"})
	report(err)
	_, err = firstEven(ports)
	report(err)
	_, err = stringify(ports)
	report(err)
	_, err = labelOdd(ports)
	report(err)
	_, err = total(ports)
	report(err)
	report(validateNames([]validate.Name{"ada"}))
	report(validateIDs([]validate.UserID{1, 2}))
	_, err = collectPorts(src)
	report(err)
	report(firstBad(src))
	tree := &seq.Node[validate.Name]{Val: "root", Kids: []*seq.Node[validate.Name]{{Val: ""}}}
	report(walkNames(tree))
	fmt.Println(seq./*@keys*/Keys(map[string]int{"a": 1}))
	return nil
}

func runCaching() error {
	scores := cache.New[string, int](2)
	names := cache.New[validate.UserID, validate.Name](8)
	sessions := cache.NewTTL[string, int](4)
	report(fill(scores, []Pair[string, int]{{Key: "a", Val: 1}, {Key: "a", Val: 2}}))
	_, err := score(scores, "zz")
	report(err)
	_, err = lazy(scores, "b")
	report(err)
	_, err = session(sessions, "s1")
	report(err)
	_, err = nameOf(names, 3)
	report(err)
	_, err = viaEqual(scores, "q")
	report(err)
	report(tag(scores, "q"))
	report(skipMissing(scores, "q"))
	_, err = viaResult("8080")
	report(err)
	_, err = firstName(result.Some(validate.Name("ada")))
	report(err)
	return flushAll(scores, names, sessions)
}

func runRepo() error {
	users := repo.New[validate.UserID, model.User](16)
	tags := repo.New[string, model.Tag](16)
	report(saveUser(users, model.User{ID: 1, Name: "ada"}))
	report(saveTags(tags, []model.Tag{"go", "generics"}))
	_, err := findUser(users, 2)
	return err
}

func runFailures() error {
	report(onlyStrings(1, "a"))
	report(notRange(1, "a"))
	report(eqRange(1, "a"))
	report(describe(1, "a"))
	report(liftBoth(1))
	report(liftNil())
	report(dropInts(1, "a"))
	report(viaAsType(1, "a"))
	report(skipRange(1, "a"))
	return deepWrap()
}

func report(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "generics:", err)
	}
}
