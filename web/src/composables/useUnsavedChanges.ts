import { onBeforeUnmount, watch, type Ref } from "vue";
import { onBeforeRouteLeave } from "vue-router";

// Guards a view that holds unsaved edits.
//
// The template editor already tracked whether it had unsaved work and showed a
// badge, but nothing stopped you leaving: a click on the sidebar discarded it
// silently. This covers both ways out — an in-app route change, which we can ask
// about ourselves, and closing or reloading the tab, where the browser shows its
// own generic prompt and all we can do is ask for it.
export function useUnsavedChanges(
  isDirty: Ref<boolean>,
  confirmLeave: (message: string) => Promise<boolean>,
  message = "You have unsaved changes. Leave without saving?"
) {
  function onBeforeUnload(e: BeforeUnloadEvent) {
    if (!isDirty.value) return;
    e.preventDefault();
    // Browsers ignore custom text here and show their own wording, but the
    // assignment is still what triggers the prompt in older engines.
    e.returnValue = message;
  }

  // The listener is only attached while there is something to lose, so a clean
  // editor never makes the browser hesitate on reload.
  watch(
    isDirty,
    (dirty) => {
      if (dirty) window.addEventListener("beforeunload", onBeforeUnload);
      else window.removeEventListener("beforeunload", onBeforeUnload);
    },
    { immediate: true }
  );

  onBeforeUnmount(() => window.removeEventListener("beforeunload", onBeforeUnload));

  onBeforeRouteLeave(async () => {
    if (!isDirty.value) return true;
    return await confirmLeave(message);
  });
}
