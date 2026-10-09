// Element Plus' table renders its body from measured column widths, and jsdom
// has no layout engine: with every measurement zero it renders a header and no
// rows, so a page test would assert against an empty table and pass for the
// wrong reason. Giving jsdom fake widths is not enough either -- the body still
// never materializes.
//
// These stubs keep the part of the contract the pages actually use: a `data`
// array, columns whose #default slot receives a row, and an `empty-text` prop
// for the no-rows case. What the assertions then cover is the page: which row
// each action carries, which actions a role is offered, what the empty state
// says. What they deliberately do not cover is the table's own rendering --
// column widths, the fixed right-hand column, row hover -- which the console
// smoke checks in a real browser, where measurements mean something.
/* eslint-disable vue/one-component-per-file -- the table and its column are one stub */
import { computed, defineComponent, h, inject, provide, type ComputedRef } from "vue";

const rowsKey = Symbol("stub-table-rows");

export const ElTableStub = defineComponent({
  name: "ElTable",
  props: {
    data: { type: Array, default: () => [] },
    emptyText: { type: String, default: "" },
  },
  setup(props, { slots }) {
    provide(rowsKey, computed(() => props.data as unknown[]));
    return () =>
      h("div", { class: "el-table" }, props.data.length
        ? slots.default?.()
        : h("div", { class: "el-table__empty-text" }, props.emptyText));
  },
});

export const ElTableColumnStub = defineComponent({
  name: "ElTableColumn",
  props: {
    prop: { type: String, default: "" },
    label: { type: String, default: "" },
  },
  setup(props, { slots }) {
    const rows = inject<ComputedRef<unknown[]>>(rowsKey);
    return () => h(
      "div",
      { class: "el-table__column", "data-column": props.label },
      (rows?.value ?? []).map((row, index) => h(
        "div",
        { class: "el-table__cell", "data-row-index": index },
        // A column without a slot (prop only, as the type column is) renders
        // the row's value, the way the real table does.
        slots.default
          ? slots.default({ row })
          : String((row as Record<string, unknown>)[props.prop] ?? ""),
      )),
    );
  },
});

// Passed as mount(Component, { global: { stubs: tableStubs } }).
export const tableStubs = {
  ElTable: ElTableStub,
  ElTableColumn: ElTableColumnStub,
};
