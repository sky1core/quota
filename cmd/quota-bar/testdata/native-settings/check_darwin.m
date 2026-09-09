#import "settings_window.source"
extern void nativeCheckReopen(void);
extern void nativeCheckFinish(void);
static id originalDelegate;
static NSWindow *originalWindow;
static int stage;
static int ticks;
static void require(BOOL value, const char *reason) {
 if (!value) { fprintf(stderr,"FAIL: %s\n",reason); exit(1); }
}
void native_check_start(void) { originalDelegate=NSApp.delegate; }
static void tick(void) {
 QuotaSettingsController *c=quotaSettingsController;
 require(++ticks<100,"native test timeout");
 require(NSApp.delegate==originalDelegate,"application delegate preserved");
 if(stage==0 && !c.window.keyWindow && ticks<50) { dispatch_after(dispatch_time(DISPATCH_TIME_NOW,100*NSEC_PER_MSEC),dispatch_get_main_queue(),^{tick();}); return; }
 if(stage==0) {
  require(c.window.visible,"window visible");
  require(NSApp.activationPolicy==NSApplicationActivationPolicyAccessory,"accessory activation policy"); if (!c.window.keyWindow) fprintf(stderr,"UNVERIFIED: OS activation from automated launch; accessory policy confirmed\n");
  require([c.window.firstResponder isKindOfClass:NSTextView.class],"initial editor focus");
  require(c.tabs.numberOfTabViewItems==3,"three native tabs");
  require(c.accounts.count==3,"snapshot accounts");
  require(!c.accounts[0].remove.enabled && !c.accounts[0].key.enabled && c.accounts[0].minimum.enabled,"default row constraints");
  originalWindow=c.window;
  [c.tabs selectTabViewItemAtIndex:1];
  [c.tabs selectTabViewItemAtIndex:2];
  c.activeMinutes.stringValue=@"1.2";
  [c save:nil];
  require(!c.applying && !c.errorScroll.hidden,"invalid number stays editable");
  c.activeMinutes.stringValue=@"7";
  [c addAccount:nil];
  require(c.accounts.count==4,"add account");
  [c save:nil];
  require(!c.applying && !c.errorScroll.hidden,"invalid account stays editable");
  [c removeAccount:c.accounts.lastObject.remove];
  require(c.accounts.count==3,"remove draft account");
  c.displayChecks[0].state=NSControlStateValueOff;
  c.displayChecks[1].state=NSControlStateValueOn;
  c.message.string=@"Edited message.";
  [c save:nil];
  require(c.applying && !c.saveButton.enabled && !c.cancelButton.enabled,"save/cancel disabled during apply");
  require(![c windowShouldClose:c.window],"close blocked during apply");
  require(!c.activeMinutes.enabled && !c.message.editable,"editing blocked during apply");
  stage=1;
 } else if(stage==1 && !c.applying) {
  require(c.window.visible && [c.errorText.string containsString:@"Test apply error"],"callback error shown and window stays open");
  require(c.activeMinutes.integerValue==7 && c.saveButton.enabled,"draft preserved and retry enabled");
  [c save:nil];
  stage=2;
 } else if(stage==2 && c.token==0) {
  require(!c.window.visible,"successful apply closes");
  nativeCheckReopen();
  stage=3;
 } else if(stage==3 && c.token!=0) {
  require(c.window==originalWindow,"one retained reusable window");
  require(c.activeMinutes.integerValue==3,"fresh snapshot on reopen");
  c.activeMinutes.stringValue=@"99";
  [c cancel:nil];
  require(c.token==0 && !c.window.visible,"cancel closes and releases callback");
  fprintf(stdout,"PASS: native tabs, field editor, field validation, account rows, disabled apply, error display, window reuse, cancellation\n");
  nativeCheckFinish();
  return;
 }
 dispatch_after(dispatch_time(DISPATCH_TIME_NOW,100*NSEC_PER_MSEC),dispatch_get_main_queue(),^{tick();});
}
void native_check_schedule(void) {
 dispatch_after(dispatch_time(DISPATCH_TIME_NOW,300*NSEC_PER_MSEC),dispatch_get_main_queue(),^{tick();});
}
