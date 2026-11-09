// Playwright regression snippet. Requires a logged-in console with at least one charge.
async (page) => {
 await page.reload();
 await page.getByRole('button',{name:'Cobranças',exact:true}).click();
 await page.getByRole('button',{name:'Ver detalhes',exact:true}).first().click();
 const detail=page.getByRole('heading',{name:'Detalhes da cobrança',exact:true});
 await detail.waitFor();
 await page.waitForTimeout(6200);
 if(await detail.count()!==1)throw new Error('Automatic refresh closed charge details');
 const historyResponse=page.waitForResponse(r=>r.url().endsWith('/history'));
 await page.getByRole('button',{name:'Atualizar',exact:true}).click();
 await historyResponse;
 await detail.waitFor();
 await page.getByRole('button',{name:'Voltar às cobranças',exact:true}).click();
 await page.getByRole('heading',{name:'Cobranças do projeto',exact:true}).waitFor();
 // A late detail response must not replace another page after explicit navigation.
 let release,received,complete;
 const held=new Promise(r=>received=r),done=new Promise(r=>complete=r);
 await page.route('**/history',async route=>{const response=await route.fetch();received();await new Promise(r=>release=r);await route.fulfill({response});complete();});
 await page.getByRole('button',{name:'Ver detalhes',exact:true}).first().click();
 await held;
 await page.getByRole('button',{name:'Planos',exact:true}).click();
 release();await done;await page.waitForTimeout(300);
 if(await detail.count()!==0)throw new Error('Late history response replaced navigation');
 await page.getByRole('heading',{name:'Planos disponíveis',exact:true}).waitFor();
 await page.unrouteAll({behavior:'wait'});
 await page.getByRole('button',{name:'Cobranças',exact:true}).click();
 const listRefresh=page.waitForResponse(r=>r.url().includes('/v1/charges'));
 await listRefresh;
 return {detailsSurvivedAutomaticRefresh:true,manualRefreshKeptDetails:true,backButtonWorked:true,lateResponseIgnored:true,listAutomaticRefreshResumed:true};
}
