// Command batch-apply turns structured research JSON into ADR, task and eval
// documents. It is the single tool used to create a wave of cards locally;
// research agents only produce JSON, the orchestrator never reads raw reports.
// Flag parsing is the only work done here: file and subprocess access lives in
// internal/infra/batchapply.
//
// Language policy (HD-053, ADR 0304): the interface and generated artifacts
// are Russian. Identifiers, flags and status values stay English so scripts
// and code can rely on them. internal/infra/batchapply declares the same policy
// as constants and pins the output with tests.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/refusedguy/beacon/internal/infra/batchapply"
)

func main() {
	flag.Usage = func() {
		w := flag.CommandLine.Output()
		fmt.Fprintln(w, "batch-apply: языковая политика (HD-053, ADR 0304)")
		fmt.Fprintln(w, "  интерфейс и артефакты: русский")
		fmt.Fprintln(w, "  технические имена, флаги и статусы: английский")
		flag.PrintDefaults()
	}
	dir := flag.String("dir", "", "каталог с JSON-файлами пакета")
	dryRun := flag.Bool("dry-run", false, "печатать файлы без записи")
	check := flag.Bool("check", false, "только проверить JSON и напечатать краткую сводку")
	mergePlan := flag.Bool("merge-plan", false, "слить PR из merge_decisions после проверки CI")
	confirmMerge := flag.Bool("confirm-merge", false, "подтвердить слияние PR из merge_decisions")
	quiet := flag.Bool("quiet", false, "не печатать каждый созданный файл")
	commit := flag.Bool("commit", false, "git add и commit созданных docs")
	facts := flag.Bool("facts", false, "печатать индекс чисел и их владельцев")
	flag.Parse()
	os.Exit(batchapply.Run(batchapply.Options{
		Dir:          *dir,
		DryRun:       *dryRun,
		Check:        *check,
		MergePlan:    *mergePlan,
		ConfirmMerge: *confirmMerge,
		Quiet:        *quiet,
		Commit:       *commit,
		Facts:        *facts,
	}, os.Stdout, os.Stderr))
}
