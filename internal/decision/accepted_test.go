package decision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestAcceptedPlanRevisionSourceAndAuthority(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	file := filepath.Join(root, "handler.go")
	if err := os.WriteFile(file, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := AcceptedPlanStore{Directory: t.TempDir()}
	intent := IntentContext{Version: 1, Repository: "repo", ActiveObjective: "Repair handler validation", ScopeFiles: []string{"handler.go"}, Constraints: []string{"Preserve the public API"}}
	ref, err := store.Accept(ctx, "task", root, intent)
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixtureConfig("repo")
	cfg.Repositories[0].Path = root
	planner := Planner{Config: cfg, AcceptedPlans: &store}
	plan, err := planner.Build(Request{Text: "hazlo", Repository: "repo", AcceptedPlan: &ref})
	if err != nil || !plan.Valid || plan.Intent != KindChange || !plan.ContextUsed {
		t.Fatalf("resolved plan: %+v, %v", plan, err)
	}
	for _, step := range plan.Workflow.Steps {
		for _, effect := range step.Permission.Effects {
			if effect == contract.EffectWrite || effect == contract.EffectExternal {
				t.Fatalf("acceptance granted effect: %+v", step)
			}
		}
	}
	assertBlocked := func(req Request) {
		t.Helper()
		p, err := planner.Build(req)
		if err != nil || p.Resolution != ResolutionNeedsContext || p.Valid || len(p.Workflow.Steps) != 0 {
			t.Fatalf("not blocked: %+v %v", p, err)
		}
	}
	assertBlocked(Request{Text: "hazlo", Repository: "other", AcceptedPlan: &ref})
	assertBlocked(Request{Text: "hazlo", AcceptedPlan: &ref})
	assertBlocked(Request{Text: "hazlo", Repository: "repo", AcceptedPlan: &ref, Context: &intent})
	assertBlocked(Request{Text: "hazlo", Repository: "repo", AcceptedPlan: &ref, Files: []string{"outside.go"}})
	bad := ref
	bad.Revision = "wrong"
	assertBlocked(Request{Text: "hazlo", Repository: "repo", AcceptedPlan: &bad})
	intent.ActiveObjective = "Repair a different validation rule"
	newRef, err := store.Accept(ctx, "task", root, intent)
	if err != nil || newRef.Revision == ref.Revision {
		t.Fatalf("replacement: %+v %v", newRef, err)
	}
	assertBlocked(Request{Text: "hazlo", Repository: "repo", AcceptedPlan: &ref})
	if err := os.WriteFile(file, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertBlocked(Request{Text: "hazlo", Repository: "repo", AcceptedPlan: &newRef})
}

func TestAcceptedPlanInputAndFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := AcceptedPlanStore{Directory: t.TempDir()}
	intent := IntentContext{Version: 1, Repository: "repo", ActiveObjective: "Implement validation"}
	for _, id := range []string{"", "../escape", "/tmp/escape", "a/b", "a\\b"} {
		if _, err := store.Accept(ctx, id, root, intent); err == nil {
			t.Fatalf("accepted id %q", id)
		}
	}
	bad := intent
	bad.AcceptedPlanCurrent = true
	if _, err := store.Accept(ctx, "task", root, bad); err == nil {
		t.Fatal("accepted caller freshness assertion")
	}
	bad = intent
	bad.ScopeFiles = []string{"../escape"}
	if _, err := store.Accept(ctx, "task", root, bad); err == nil {
		t.Fatal("accepted escaped scope")
	}
	if _, err := (AcceptedPlanStore{Directory: root}).Accept(ctx, "task", root, intent); err == nil {
		t.Fatal("allowed store inside source")
	}
	ref, err := store.Accept(ctx, "task", root, intent)
	if err != nil {
		t.Fatal(err)
	}
	name, err := store.filename(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt permissions: %v %v", info, err)
	}
	if err := os.WriteFile(name, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(ctx, ref, "repo", root); err == nil {
		t.Fatal("accepted modified receipt")
	}
	if _, err := store.Resolve(ctx, AcceptedPlanReference{ID: "missing", Revision: "abc"}, "repo", root); err == nil {
		t.Fatal("accepted missing receipt")
	}
}

func TestSpanishDecisionRequests(t *testing.T) {
	for _, tc := range []struct {
		text string
		want Kind
	}{
		{"¿Cómo podemos mejorar la validación?", KindPlan},
		{"Como lo hacemos mas preciso", KindPlan},
		{"Ahora que hacemos?", KindPlan},
		{"¿Qué hay que hacer para migrar?", KindPlan},
		{"Vale, revisa que las comprobaciones han terminado", KindSearch},
		{"Comprueba si existe una función para cambiar el formato", KindSearch},
		{"Verifica que los planes se guardan", KindSearch},
		{"Verifica si no cambia el resultado", KindSearch},
		{"Comprueba si no actualiza el estado", KindSearch},
		{"Verifica si falla la validación y luego corrige el error", KindChange},
		{"Verifica si el proceso lee y cambia el formato", KindSearch},
		{"Comprueba que compila; si no, corrige el fallo", KindChange},
		{"Comprueba si existe una función para cambiar el formato y no modifiques nada", KindSearch},
		{"¿Has terminado de corregir el fallo?", KindSearch},
		{"¿Has terminado de corregir el fallo? Gracias", KindSearch},
		{"¿Has terminado de corregir el fallo?!", KindSearch},
		{"¿Has terminado de corregir el fallo? 👍", KindSearch},
		{"¿Has terminado de corregir el fallo? por favor", KindSearch},
		{"Have you finished fixing the bug?", KindSearch},
		{"Are there any bugs left to fix?", KindSearch},
		{"Are there any fixes left to implement?", KindSearch},
		{"Is it fixed? Please", KindSearch},
		{"Is it fixed? Thanks", KindSearch},
		{"Is it fixed? Thank you", KindSearch},
		{"Is it fixed? Cheers", KindSearch},
		{"Anything else? Is it fixed? Cheers", KindSearch},
		{"¿Has terminado de corregir el fallo? Un saludo", KindSearch},
		{"¿Alguna novedad? ¿Has terminado de corregir el fallo? Un saludo", KindSearch},
		{"Is it fixed? Fix the bug", KindChange},
		{"Has terminado de corregir el fallo", KindSearch},
		{"Has terminado de revisar: corrige el fallo", KindChange},
		{"Has terminado de revisar\nCorrige el fallo", KindChange},
		{"Has terminado de revisar, corrige el fallo", KindChange},
		{"Has terminado de revisar y corrige el fallo", KindChange},
		{"¿Has terminado de revisar y cambia el resultado?", KindSearch},
		{"¿Has terminado de revisar y actualiza correctamente el estado?", KindSearch},
		{"¿Has terminado de revisar, cambia el resultado?", KindSearch},
		{"¿Alguna novedad? ¿Has terminado de revisar y cambia el resultado?", KindSearch},
		{"Has terminado de corregir el fallo. Corrige el siguiente", KindChange},
		{"Hay cambios pendientes. Corrige el fallo", KindChange},
		{"Por favor, ¿has terminado de corregir el fallo?", KindSearch},
		{"Y después? ¿Ya has terminado de arreglar la incidencia?", KindSearch},
		{"¿No hay incidencias pendientes para arreglar?", KindSearch},
		{"¿Hay que corregir el fallo?", KindPlan},
		{"¿No hay que implementar P12?", KindPlan},
		{"¿No hay incidencias? Corrige el fallo ahora", KindChange},
		{"¿Has terminado de corregirlo? Si no, corrige el fallo", KindChange},
		{"¿Has terminado de corregir el fallo y, si no, corrígelo?", KindChange},
		{"¿Has terminado? Si no, corrígelo mañana", KindPlan},
		{"Añade un P nuevo después del P12", KindPlan},
		{"Tenemos que añadir un P más junto al P12", KindPlan},
		{"Añade P13 después de P12", KindPlan},
		{"Add P13 after P12", KindPlan},
		{"Add P13 after P12 and update the plan", KindPlan},
		{"Add P13 after P12 and implement it", KindChange},
		{"Add P13 after P12 and fix the bug", KindChange},
		{"Add P13 after P12 and update it", KindPlan},
		{"Add P13 after P12 and edit its wording", KindPlan},
		{"Add P13 after P12 and update its description", KindPlan},
		{"Añade P13 después de P12 y actualiza su descripción", KindPlan},
		{"Add P13 after P12 and update its code", KindChange},
		{"Add P13 after P12 to the codebase", KindChange},
		{"Añade el P13 después del P12", KindPlan},
		{"Agrega P13 tras P12", KindPlan},
		{"Añade un P nuevo después del P12 y actualiza el plan", KindPlan},
		{"Añade un P nuevo después del P12 y corrige el fallo ahora", KindChange},
		{"Añade P13 después de P12 y aplícalo", KindChange},
		{"Añade P13 después de P12 y corrígelo", KindChange},
		{"Añade P13 después de P12 y impleméntalo", KindChange},
		{"Añade P13 después de P12 y hazlo", KindChange},
		{"Añade el proceso P12 al sistema", KindChange},
		{"Explica cómo podemos cambiar la validación", KindUnderstand},
		{"No cambies nada, explica el comportamiento", KindUnderstand},
		{"Cómo funciona el planificador", KindUnderstand},
		{"Corrige la validación del formato", KindChange},
		{"Planifica la migración y luego implementa las pruebas", KindChange},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got := infer(tc.text); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	for _, text := range []string{"arreglalo", "arréglalo", "corrígelo", "aplícalo", "Vale, hazlo", "corrige eso", "implementa esto", "aplica eso", "haz eso", "cambia aquello", "modifica esto", "actualiza eso"} {
		p, err := (Planner{Config: fixtureConfig("repo")}).Build(Request{Text: text, Repository: "repo", StandingEffects: []contract.Effect{contract.EffectWrite}})
		if err != nil || p.Resolution != ResolutionNeedsContext || len(p.Workflow.Steps) != 0 {
			t.Fatalf("unbound %q: %+v %v", text, p, err)
		}
	}
}

func TestAcceptedPlanRejectsStoreInsideParentGitWorktree(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "app")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "code.go"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	store := AcceptedPlanStore{Directory: filepath.Join(root, ".cfg", "accepted-plans")}
	if _, err := store.Accept(t.Context(), "test", nested, IntentContext{Version: 1, Repository: "repo", ActiveObjective: "Improve validation"}); err == nil {
		t.Fatal("allowed a receipt inside parent Git worktree")
	}
}
