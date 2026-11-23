package hibapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/termsafe"
)

const FeatureRecipes = "recipes"

// MaxRecipeCommand bounds a recipe's JSON-encoded argv.
const MaxRecipeCommand = 16 << 10

type Recipe struct {
	Name           string       `json:"name"`
	Command        []string     `json:"command"`
	Image          string       `json:"image,omitempty"`
	Size           string       `json:"size,omitempty"`
	TimeoutSeconds int64        `json:"timeoutSeconds,omitempty"`
	Publish        task.Publish `json:"publish,omitempty"`
}

type RecipeReply struct {
	Recipe        *Recipe  `json:"recipe,omitempty"`
	Recipes       []Recipe `json:"recipes,omitzero"`
	DefaultRecipe string   `json:"defaultRecipe,omitempty"`
}

func (r Recipe) Check() error {
	if err := CheckName(r.Name); err != nil {
		return err
	}
	if len(r.Command) == 0 {
		return errors.New("give a command after --")
	}
	for i, arg := range r.Command {
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) || (i == 0 && arg == "") {
			return errors.New("command needs a non-empty executable and UTF-8 arguments without NUL")
		}
	}
	data, err := json.Marshal(r.Command)
	if err != nil {
		return err
	}
	if len(data) > MaxRecipeCommand {
		return fmt.Errorf("recipe command JSON exceeds %d bytes", MaxRecipeCommand)
	}
	if r.Image != "" {
		if err := CheckName(r.Image); err != nil {
			return err
		}
	}
	if r.TimeoutSeconds < 0 || r.TimeoutSeconds > int64((1<<63-1)/time.Second) {
		return errors.New("timeout must be non-negative whole seconds and fit in a duration")
	}
	if r.Publish != "" && r.Publish != task.PublishNone {
		return errors.New("recipe publication must be none")
	}
	return nil
}

func PrintRecipe(w io.Writer, r Recipe) error {
	_, err := fmt.Fprintf(w, "%s\n  command %s\n  image %s\n  size %s\n  timeout %s\n",
		termsafe.Line(r.Name), termsafe.Line(fmt.Sprintf("%q", r.Command)),
		termsafe.Line(r.Image), termsafe.Line(r.Size), time.Duration(r.TimeoutSeconds)*time.Second)
	return err
}
