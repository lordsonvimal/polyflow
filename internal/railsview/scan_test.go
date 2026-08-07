package railsview

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// scanERB runs the render scanner the way the linker does for a template: over
// the virtualRuby half of the split, never the raw file.
func scanERB(src string) []Render {
	_, ruby := SplitERB([]byte(src))
	return ScanRenders(ruby)
}

// TestScanRenders_Spellings covers the four Rails forms nextGen actually uses.
func TestScanRenders_Spellings(t *testing.T) {
	got := scanERB(`<div>
  <%= render "shared/nav_bar" %>
  <%= render partial: "row", collection: @rows %>
  <%= render :partial => "legacy/row" %>
  <%= render layout: "shared/panel" do %>
    <p>body</p>
  <% end %>
</div>`)

	require.Equal(t, []Render{
		{Kind: RenderPositional, Spec: "shared/nav_bar", Line: 2},
		{Kind: RenderPartial, Spec: "row", Line: 3, Collection: true},
		{Kind: RenderPartial, Spec: "legacy/row", Line: 4},
		{Kind: RenderLayout, Spec: "shared/panel", Line: 5},
	}, got)
}

// TestScanRenders_NonViewIsNotAClue: `render json:` states there is no view.
// Ledgering it would fabricate a clue, the same reason K.3 drops require_self.
func TestScanRenders_NonViewIsNotAClue(t *testing.T) {
	require.Empty(t, ScanRenders([]byte(`
def create
  render json: @user, status: :created
  render plain: "ok"
  render head: :no_content
end`)))
}

// TestScanRenders_CommentsAndStrings is the false-positive class: a commented
// ERB tag, a Ruby comment, and the word inside a flash message must all read as
// text, not as a call.
func TestScanRenders_CommentsAndStrings(t *testing.T) {
	require.Empty(t, scanERB(`
<%# render "shared/dead_partial" %>
<% # render "shared/also_dead" %>
<% flash[:notice] = "could not render the report" %>
<%= form.render_to_string "x" %>
<%= presenter.render "x" %>
`))
}

// TestScanRenders_Multiline: a parenthesised call routinely puts its argument
// on the next line, and the reported line must be the argument's own.
func TestScanRenders_Multiline(t *testing.T) {
	got := scanERB(`<%= render(
      partial: "deliverables/task_card",
      collection: @tasks
    ) %>`)
	require.Equal(t, []Render{
		{Kind: RenderPartial, Spec: "deliverables/task_card", Line: 2, Collection: true},
	}, got)
}

// TestScanRenders_TrailingModifierIsStillStatic: `render "x" if cond` names a
// fully static partial; rejecting it over the modifier would ledger a clue the
// scanner can read perfectly well.
func TestScanRenders_TrailingModifierIsStillStatic(t *testing.T) {
	got := scanERB(`<%= render "shared/banner" if current_user %>`)
	require.Equal(t, []Render{{Kind: RenderPositional, Spec: "shared/banner", Line: 1}}, got)
}

// TestScanRenders_DynamicIsFlagged: an object, a variable or an interpolated
// name is ledgered, never guessed (phases.md #12).
func TestScanRenders_DynamicIsFlagged(t *testing.T) {
	got := scanERB(`<%= render @execution_item %>
<%= render "tabs/#{tab_name}" %>`)
	require.Len(t, got, 2)
	for _, r := range got {
		require.True(t, r.Dynamic, "%+v", r)
	}
	require.Equal(t, "@execution_item", got[0].Spec)
}

// TestScanRenders_ControllerTemplate: the same scanner reads a controller; only
// the resolver's default kind differs.
func TestScanRenders_ControllerTemplate(t *testing.T) {
	got := ScanRenders([]byte(`class ReportsController < ApplicationController
  def show
    render "reports/detail"
  end
end`))
	require.Equal(t, []Render{{Kind: RenderPositional, Spec: "reports/detail", Line: 3}}, got)
}

// TestScanRenders_SymbolTemplates: `render :index` and `render action: :fail`
// are as static as their quoted forms, and are a quarter of nextGen's
// controller renders. Reading them as dynamic ledgers a clue that is right there.
func TestScanRenders_SymbolTemplates(t *testing.T) {
	got := ScanRenders([]byte(`def create
  render :new
  render action: :fail, layout: false
  render action: "edit"
  render :no_settings if @organization.nil?
end`))

	require.Equal(t, []Render{
		{Kind: RenderPositional, Spec: "new", Line: 2},
		{Kind: RenderTemplate, Spec: "fail", Line: 3},
		{Kind: RenderTemplate, Spec: "edit", Line: 4},
		{Kind: RenderPositional, Spec: "no_settings", Line: 5},
	}, got)
}

// TestScanRenders_LayoutToggleNamesNothing: `layout: false` turns the layout
// off. There is no view to ledger — 17 of nextGen's controller renders spell it.
func TestScanRenders_LayoutToggleNamesNothing(t *testing.T) {
	require.Empty(t, ScanRenders([]byte("render layout: false\nrender layout: nil\n")))
}

// TestScanRenders_EveryKeywordIsAView: a call can name two, and taking only the
// first would be the fan-out bug (phases.md #1).
func TestScanRenders_EveryKeywordIsAView(t *testing.T) {
	require.Equal(t, []Render{
		{Kind: RenderPartial, Spec: "row", Line: 1},
		{Kind: RenderLayout, Spec: "sidebar_layout", Line: 1},
	}, ScanRenders([]byte(`render partial: "row", layout: "sidebar_layout"`)))
}

func TestScanReactComponents_Literals(t *testing.T) {
	_, ruby := SplitERB([]byte(`<div>
  <%= react_component("ContainerTypesContainer", { container_types: @container_types }) %>
  <%= react_component(
        "DirTreeContainer",
        { root: @root }
      ) %>
  <%= react_component "OnboardingTip" %>
  <%# react_component("DeadContainer") %>
</div>`))

	require.Equal(t, []ReactComponent{
		{Name: "ContainerTypesContainer", Line: 2},
		{Name: "DirTreeContainer", Line: 4},
		{Name: "OnboardingTip", Line: 7},
	}, ScanReactComponents(ruby))
}

func TestScanReactComponents_DynamicIsFlagged(t *testing.T) {
	got := ScanReactComponents([]byte(`<%= react_component(component_name, opts) %>`))
	require.Len(t, got, 1)
	require.True(t, got[0].Dynamic)
	require.Equal(t, "component_name", got[0].Name)
}

func TestScanRenders_Deterministic(t *testing.T) {
	src := `<%= render "a/b" %>
<%= render partial: "c" %>
<%= react_component("X") %>`
	first := scanERB(src)
	for i := 0; i < 5; i++ {
		require.Equal(t, first, scanERB(src))
	}
}
