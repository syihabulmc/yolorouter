import { onScopeDispose } from 'vue'
import { useDialog } from 'naive-ui'

type DialogApi = ReturnType<typeof useDialog>

export interface ConfirmDisableCopy {
  title: string
  content: string
  positiveText: string
  negativeText: string
}

// Shared "toggle management_status, confirm before disabling" flow used by
// the model and provider pages: enabling proceeds immediately, disabling
// shows a confirm dialog first. `proceed` performs the actual toggle +
// reload and owns its own error handling.
//
// The copy is an async builder so the dialog can carry a fetched impact
// preview. That makes staleness possible: the dialog is global and `proceed`
// captures the clicked row, so a preview that resolves after the component
// is gone — or after a newer click started its own preview — must be
// discarded, not shown. Confirming a dialog about something no longer on
// screen is exactly the mistake the preview exists to prevent. Hence this is
// a composable: the returned function drops late arrivals via a per-scope
// disposal flag and a per-call generation. Builders must resolve (degrading
// their content) rather than reject, or no dialog appears and the action is
// silently dropped. Content renders pre-line because previews are
// multi-line.
export function useConfirmedStatusToggle(dialog: DialogApi) {
  let generation = 0
  let disposed = false
  onScopeDispose(() => {
    disposed = true
  })
  return function toggleStatusWithConfirm(
    enable: boolean,
    confirmCopy: () => Promise<ConfirmDisableCopy>,
    proceed: () => Promise<void>,
  ) {
    if (enable) {
      void proceed()
      return
    }
    const thisCall = ++generation
    void confirmCopy().then((copy) => {
      if (disposed || thisCall !== generation) return
      dialog.warning({ ...copy, style: 'white-space: pre-line', onPositiveClick: proceed })
    })
  }
}
